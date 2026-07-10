package transform

import (
	"encoding/json"
	"fmt"
	"io"
)

type Transformer interface {
	Transform(r io.Reader) (io.Reader, error)   // encode (download path)
	Untransform(r io.Reader) (io.Reader, error) // decode (upload path)
}

type TransformerFactory func(args json.RawMessage) (Transformer, error)

var transformers = map[string]TransformerFactory{
	"id":       newIDTransformer,
	"xor":      newXORTransformer,
	"aes-ctr":  newAESCTRTransformer,
	"pdf":      newPDFTransformer,
	"html-b64": newHTMLB64Transformer,
}

func New(name string, args json.RawMessage) (Transformer, error) {
	factory, ok := transformers[name]
	if !ok {
		return nil, fmt.Errorf("unknown transform %q", name)
	}
	return factory(args)
}
