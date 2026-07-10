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

func newIDTransformer(_ json.RawMessage) (Transformer, error) {
	return &idTransformer{}, nil
}
