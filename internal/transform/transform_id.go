package transform

import (
	"encoding/json"
	"io"
)

type idTransformer struct{}

func (t *idTransformer) Transform(r io.Reader) (io.Reader, error) {
	return r, nil
}

func (t *idTransformer) Untransform(r io.Reader) (io.Reader, error) {
	return r, nil
}

func (t *idTransformer) EncodedLen(plaintextLen int64) int64 {
	return plaintextLen
}

func newIDTransformer(_ json.RawMessage) (Transformer, error) {
	return &idTransformer{}, nil
}
