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

// SizedTransformer is implemented by transforms whose Transform output
// length is a deterministic function of the input length, computable
// without materializing the output. Chunked WS downloads use this to
// announce a chunk's transformed byte length before streaming its frames;
// a transform that doesn't implement it (currently only pdf, whose output
// size depends on internal PDF library overhead) is instead fully buffered
// so its length can be measured directly.
type SizedTransformer interface {
	Transformer
	EncodedLen(plaintextLen int64) int64
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
