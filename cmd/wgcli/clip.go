package main

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"warpgate/internal/transform"
)

// clipSourceCLI marks notes created by wgcli.
const clipSourceCLI = "cli"

// clipAttachmentMeta mirrors the server's AttachmentMeta (db.go).
type clipAttachmentMeta struct {
	ID      int64  `json:"id"`
	Mime    string `json:"mime"`
	Size    int64  `json:"size"`
	Caption string `json:"caption,omitempty"`
}

// clipNoteMeta mirrors the server's NoteMeta (db.go).
type clipNoteMeta struct {
	ID          int64                `json:"id"`
	TS          int64                `json:"ts"`
	Title       string               `json:"title"`
	Source      string               `json:"source,omitempty"`
	Data        string               `json:"data"`
	Attachments []clipAttachmentMeta `json:"attachments"`
}

type clipStateResponse struct {
	Rev     int64          `json:"rev"`
	Changed []clipNoteMeta `json:"changed"`
	Ids     []int64        `json:"ids"`
	Reset   bool           `json:"reset"`
}

// fetchClipState pulls the notes changed after revision since (0: all).
func fetchClipState(g *globalFlags, since int64) (*clipStateResponse, error) {
	path := "/api/clip/state?since=" + strconv.FormatInt(since, 10)
	if _, enabled := g.paramKeys(); enabled {
		path += "&e=1"
	}
	req, err := g.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var state clipStateResponse
	if err := g.decodeJSON(body, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func postClipNew(g *globalFlags, title string, data []byte) (id, ts int64, err error) {
	body, err := g.encodeBody(struct {
		Title  string `json:"title,omitempty"`
		Data   string `json:"data"`
		Source string `json:"source"`
	}{title, base64.StdEncoding.EncodeToString(data), clipSourceCLI})
	if err != nil {
		return 0, 0, err
	}

	req, err := g.newRequest(http.MethodPost, "/api/clip/new", bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var result struct {
		ID int64 `json:"id"`
		TS int64 `json:"ts"`
	}
	if err := g.decodeJSON(respBody, &result); err != nil {
		return 0, 0, err
	}
	return result.ID, result.TS, nil
}

// postClipEdit leaves the note's title unchanged when title is nil.
func postClipEdit(g *globalFlags, id int64, title *string, data []byte) error {
	return sendClipJSON(g, http.MethodPost, "/api/clip/edit", struct {
		ID    int64   `json:"id"`
		Title *string `json:"title,omitempty"`
		Data  string  `json:"data"`
	}{id, title, base64.StdEncoding.EncodeToString(data)})
}

func deleteClip(g *globalFlags, id int64) error {
	return sendClipJSON(g, http.MethodDelete, "/api/clip/delete", struct {
		ID int64 `json:"id"`
	}{id})
}

// sendClipJSON sends v as the JSON body of a clipboard request answered with
// 204 No Content.
func sendClipJSON(g *globalFlags, method, path string, v any) error {
	body, err := g.encodeBody(v)
	if err != nil {
		return err
	}
	req, err := g.newRequest(method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}
	return nil
}

// clipTextArg resolves a note's content either from -f <file> or from the
// positional argument at argIndex.
func clipTextArg(fs *flag.FlagSet, file string, argIndex int) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	if fs.NArg() <= argIndex {
		return nil, fmt.Errorf("provide note text as an argument or use -f <file>")
	}
	return []byte(fs.Arg(argIndex)), nil
}

// clipNoteOut is a note as printed by clip list/get: text decoded, newest
// first.
type clipNoteOut struct {
	ID          int64                `json:"id"`
	TS          int64                `json:"ts"`
	Title       string               `json:"title"`
	Source      string               `json:"source,omitempty"`
	Text        *string              `json:"text,omitempty"`
	Chars       *int                 `json:"chars,omitempty"`
	Attachments []clipAttachmentMeta `json:"attachments"`
}

type clipListOut struct {
	Rev   int64         `json:"rev"`
	Reset bool          `json:"reset,omitempty"`
	IDs   *[]int64      `json:"ids,omitempty"`
	Notes []clipNoteOut `json:"notes"`
}

// clipMatches reports whether q occurs case-insensitively in the note's
// title (scope all/title) or text and image captions (scope all/content).
func clipMatches(n clipNoteMeta, text, q, scope string) bool {
	q = strings.ToLower(q)
	has := func(s string) bool { return strings.Contains(strings.ToLower(s), q) }
	if scope != "content" && has(n.Title) {
		return true
	}
	if scope == "title" {
		return false
	}
	if has(text) {
		return true
	}
	for _, a := range n.Attachments {
		if has(a.Caption) {
			return true
		}
	}
	return false
}

func printClipNotes(g *globalFlags, state *clipStateResponse, notes []clipNoteMeta, brief bool) error {
	out := clipListOut{Rev: state.Rev, Reset: state.Reset, Notes: []clipNoteOut{}}
	if state.Ids != nil {
		out.IDs = &state.Ids
	}
	for _, n := range notes {
		data, err := base64.StdEncoding.DecodeString(n.Data)
		if err != nil {
			return fmt.Errorf("decoding note %d: %w", n.ID, err)
		}
		text := string(data)
		o := clipNoteOut{ID: n.ID, TS: n.TS, Title: n.Title, Source: n.Source, Attachments: n.Attachments}
		if brief {
			chars := utf8.RuneCountInString(text)
			o.Chars = &chars
		} else {
			o.Text = &text
		}
		out.Notes = append(out.Notes, o)
	}
	if g.json {
		printJSON(out)
		return nil
	}
	for _, n := range out.Notes {
		head := fmt.Sprintf("[%d] %s", n.ID, time.Unix(n.TS, 0).Format(time.RFC3339))
		if n.Source != "" {
			head += " [" + n.Source + "]"
		}
		if n.Title != "" {
			head += " " + n.Title
		}
		if brief {
			fmt.Printf("%s (%d chars, %d image(s))\n", head, *n.Chars, len(n.Attachments))
			continue
		}
		if len(n.Attachments) > 0 {
			head += fmt.Sprintf(" (%d image(s): ", len(n.Attachments))
			for i, a := range n.Attachments {
				if i > 0 {
					head += ", "
				}
				head += fmt.Sprintf("#%d %s %dB", a.ID, a.Mime, a.Size)
				if a.Caption != "" {
					head += fmt.Sprintf(" %q", a.Caption)
				}
			}
			head += ")"
		}
		fmt.Printf("%s\n%s\n\n", head, *n.Text)
	}
	if state.Reset {
		fmt.Fprintln(os.Stderr, "reset: -since was ahead of the server (new database?); showing all notes")
	}
	fmt.Fprintf(os.Stderr, "rev %d\n", state.Rev)
	return nil
}

func runClipList(args []string) error {
	fs := flag.NewFlagSet("clip list", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	since := fs.Int64("since", 0, "only notes created or edited after this revision (as printed by a previous list)")
	limit := fs.Int("limit", 0, "only the newest n notes (0 = all)")
	search := fs.String("search", "", "case-insensitive substring to match")
	scope := fs.String("scope", "all", "where -search looks: all|title|content (content includes image captions)")
	brief := fs.Bool("brief", false, "one line per note, without the text")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	if *scope != "all" && *scope != "title" && *scope != "content" {
		return fmt.Errorf("-scope must be all, title or content")
	}

	state, err := fetchClipState(g, *since)
	if err != nil {
		return err
	}
	notes := make([]clipNoteMeta, 0, len(state.Changed))
	for _, n := range state.Changed {
		if *search != "" {
			data, err := base64.StdEncoding.DecodeString(n.Data)
			if err != nil {
				return fmt.Errorf("decoding note %d: %w", n.ID, err)
			}
			if !clipMatches(n, string(data), *search, *scope) {
				continue
			}
		}
		notes = append(notes, n)
	}
	slices.SortFunc(notes, func(a, b clipNoteMeta) int {
		return cmp.Or(cmp.Compare(b.TS, a.TS), cmp.Compare(b.ID, a.ID))
	})
	if *limit > 0 && len(notes) > *limit {
		notes = notes[:*limit]
	}
	return printClipNotes(g, state, notes, *brief)
}

func runClipGet(args []string) error {
	fs := flag.NewFlagSet("clip get", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	brief := fs.Bool("brief", false, "one line per note, without the text")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli clip get [flags] <id> [<id>...]")
	}
	state, err := fetchClipState(g, 0)
	if err != nil {
		return err
	}
	byID := map[int64]clipNoteMeta{}
	for _, n := range state.Changed {
		byID[n.ID] = n
	}
	var notes []clipNoteMeta
	for _, arg := range fs.Args() {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid note id %q: %w", arg, err)
		}
		n, ok := byID[id]
		if !ok {
			return fmt.Errorf("note %d not found", id)
		}
		notes = append(notes, n)
	}
	state.Ids = nil
	return printClipNotes(g, state, notes, *brief)
}

// clipImage is one -attach of clip new, with the -caption that followed it.
type clipImage struct{ path, caption string }

type attachFlag struct{ list *[]clipImage }

func (f attachFlag) String() string { return "" }
func (f attachFlag) Set(v string) error {
	*f.list = append(*f.list, clipImage{path: v})
	return nil
}

type captionFlag struct{ list *[]clipImage }

func (f captionFlag) String() string { return "" }
func (f captionFlag) Set(v string) error {
	if len(*f.list) == 0 {
		return fmt.Errorf("-caption must follow an -attach")
	}
	(*f.list)[len(*f.list)-1].caption = v
	return nil
}

func runClipNew(args []string) error {
	fs := flag.NewFlagSet("clip new", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	file := fs.String("f", "", "read note content from this file instead of the positional argument")
	title := fs.String("title", "", "note title (optional)")
	var images []clipImage
	fs.Var(attachFlag{&images}, "attach", "image file to attach (repeatable); with at least one, the text is optional")
	fs.Var(captionFlag{&images}, "caption", "caption of the preceding -attach")
	transformName := fs.String("transform", "id", "transform to disguise the image bytes with")
	key := fs.String("key", "", "transform key (hex), where applicable; random if omitted")
	iv := fs.String("iv", "", "transform IV (hex), for aes-ctr; random if omitted")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	var data []byte
	if len(images) == 0 || *file != "" || fs.NArg() > 0 {
		var err error
		if data, err = clipTextArg(fs, *file, 0); err != nil {
			return err
		}
	}
	for _, img := range images {
		if mimeForExt(img.path) == "application/octet-stream" {
			return fmt.Errorf("%s: unsupported image type (png, jpg, gif, webp, bmp)", img.path)
		}
		if _, err := os.Stat(img.path); err != nil {
			return err
		}
	}
	var t transform.Transformer
	var targs json.RawMessage
	if len(images) > 0 {
		var err error
		if targs, err = buildTransformArgs(*transformName, *key, *iv, "", "attachment"); err != nil {
			return err
		}
		if t, err = transform.New(*transformName, targs); err != nil {
			return err
		}
	}

	// The revision before the note: "clip wait -since" from it can't miss
	// a reply, however fast it comes.
	before, err := fetchClipState(g, 0)
	if err != nil {
		return err
	}
	id, ts, err := postClipNew(g, *title, data)
	if err != nil {
		return err
	}
	atts := []clipAttachmentMeta{}
	for _, img := range images {
		meta, err := postClipAttachment(g, id, img.path, img.caption, *transformName, targs, t)
		if err != nil {
			return fmt.Errorf("note %d created with %d of %d image(s); attaching %s failed: %w", id, len(atts), len(images), img.path, err)
		}
		atts = append(atts, meta)
	}
	msg := fmt.Sprintf("created note %d at %s", id, time.Unix(ts, 0).Format(time.RFC3339))
	if len(atts) > 0 {
		msg += fmt.Sprintf(" with %d image(s)", len(atts))
	}
	msg += fmt.Sprintf("; replies: clip wait -since %d, files: wait-file -since %d", before.Rev, ts)
	g.report(map[string]any{"id": id, "ts": ts, "since": before.Rev, "attachments": atts}, "%s", msg)
	return nil
}

func runClipEdit(args []string) error {
	fs := flag.NewFlagSet("clip edit", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	file := fs.String("f", "", "read note content from this file instead of the positional argument")
	titleFlag := fs.String("title", "", "new note title; empty clears it, omitting the flag keeps the current one")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	var title *string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "title" {
			title = titleFlag
		}
	})

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli clip edit [flags] <id> [text]")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid note id %q: %w", fs.Arg(0), err)
	}
	data, err := clipTextArg(fs, *file, 1)
	if err != nil {
		return err
	}

	if err := postClipEdit(g, id, title, data); err != nil {
		return err
	}
	g.report(map[string]any{"id": id}, "updated note %d", id)
	return nil
}

func runClipDelete(args []string) error {
	fs := flag.NewFlagSet("clip delete", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli clip delete [flags] <id>")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid note id %q: %w", fs.Arg(0), err)
	}

	if err := deleteClip(g, id); err != nil {
		return err
	}
	g.report(map[string]any{"id": id}, "deleted note %d", id)
	return nil
}

func runClip(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wgcli clip <list|get|new|edit|delete|attach|get-attachment|delete-attachment|caption|wait> [flags] [args...]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runClipList(rest)
	case "get":
		return runClipGet(rest)
	case "new":
		return runClipNew(rest)
	case "edit":
		return runClipEdit(rest)
	case "delete":
		return runClipDelete(rest)
	case "attach":
		return runClipAttach(rest)
	case "get-attachment":
		return runClipGetAttachment(rest)
	case "delete-attachment":
		return runClipDeleteAttachment(rest)
	case "caption":
		return runClipCaption(rest)
	case "wait":
		return runClipWait(rest)
	default:
		return fmt.Errorf("unknown clip subcommand %q", sub)
	}
}
