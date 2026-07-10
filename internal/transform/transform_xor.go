package transform

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

type xorArgs struct {
	Key string `json:"key"`
}

type xorReader struct {
	r   io.Reader
	key []byte
	pos int
}

func (x *xorReader) Read(p []byte) (n int, err error) {
	n, err = x.r.Read(p)
	for i := range n {
		p[i] ^= x.key[x.pos%len(x.key)]
		x.pos++
	}
	return
}

type xorTransformer struct {
	key []byte
}

func (t *xorTransformer) Transform(r io.Reader) (io.Reader, error) {
	return &xorReader{r: r, key: t.key}, nil
}

func (t *xorTransformer) Untransform(r io.Reader) (io.Reader, error) {
	return t.Transform(r)
}

func (t *xorTransformer) EncodedLen(plaintextLen int64) int64 {
	return plaintextLen
}

func newXORTransformer(raw json.RawMessage) (Transformer, error) {
	var args xorArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("xor args: %w", err)
	}
	key, err := hex.DecodeString(args.Key)
	if err != nil {
		return nil, fmt.Errorf("xor key: %w", err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("xor key must not be empty")
	}
	return &xorTransformer{key: key}, nil
}
