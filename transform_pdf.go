package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

var basePDFBytes = func() []byte {
	var buf bytes.Buffer
	if err := api.Create(nil, strings.NewReader(basePDFJSON), &buf, nil); err != nil {
		panic("pdf: creating base PDF: " + err.Error())
	}
	return buf.Bytes()
}()

const basePDFJSON = `{
	"paper": "A4P",
	"pages": {
		"1": {
			"content": {
				"text": [{"value": "Hello there!", "pos": [100, 700], "font": {"name": "Helvetica", "size": 12}}]
			}
		}
	}
}`

type pdfArgs struct {
	Filename string `json:"filename"`
	Password string `json:"password"`
}

type pdfTransformer struct {
	filename string
	password string
}

func (t *pdfTransformer) Transform(r io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("pdf: reading data: %w", err)
	}

	conf := model.NewDefaultConfiguration()
	conf.Cmd = model.ADDATTACHMENTS
	ctx, err := api.ReadValidateAndOptimize(bytes.NewReader(basePDFBytes), conf)
	if err != nil {
		return nil, fmt.Errorf("pdf: reading PDF context: %w", err)
	}

	a := model.Attachment{
		Reader:   bytes.NewReader(data),
		ID:       t.filename,
		FileName: t.filename,
	}
	if err := ctx.AddAttachment(a, false); err != nil {
		return nil, fmt.Errorf("pdf: adding attachment: %w", err)
	}

	var out bytes.Buffer
	if err := api.WriteContext(ctx, &out); err != nil {
		return nil, fmt.Errorf("pdf: writing PDF: %w", err)
	}

	if t.password == "" {
		return &out, nil
	}

	conf = model.NewAESConfiguration(t.password, t.password, 256)
	var encrypted bytes.Buffer
	if err := api.Encrypt(bytes.NewReader(out.Bytes()), &encrypted, conf); err != nil {
		return nil, fmt.Errorf("pdf: encrypting: %w", err)
	}
	return &encrypted, nil
}

func (t *pdfTransformer) Untransform(r io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("pdf: reading PDF: %w", err)
	}

	conf := model.NewDefaultConfiguration()
	conf.UserPW = t.password
	conf.OwnerPW = t.password

	attachments, err := api.ExtractAttachmentsRaw(bytes.NewReader(data), "", nil, conf)
	if err != nil {
		return nil, fmt.Errorf("pdf: extracting attachment: %w", err)
	}
	if len(attachments) == 0 {
		return nil, fmt.Errorf("pdf: no attachments found in uploaded PDF")
	}
	return attachments[0], nil
}

func newPDFTransformer(raw json.RawMessage) (Transformer, error) {
	var args pdfArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("pdf args: %w", err)
		}
	}
	if args.Filename == "" {
		args.Filename = "attachment"
	}
	return &pdfTransformer{filename: args.Filename, password: args.Password}, nil
}
