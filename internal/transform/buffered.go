package transform

// MaxBufferedPlain caps the plaintext of one Transform or Untransform call of
// a transform that holds its whole input in memory (see Buffers); peak memory
// is a few times this per call. Clients split larger transfers into chunks.
const MaxBufferedPlain = 256 << 20

// pdfEncodedSlack bounds the PDF structure around an attachment.
const pdfEncodedSlack = 1 << 20

// bufferer is implemented by transforms that read their whole input into
// memory instead of streaming it.
type bufferer interface {
	buffersInput()
}

func (*pdfTransformer) buffersInput()     {}
func (*htmlB64Transformer) buffersInput() {}

// Buffers reports whether t holds a whole call's input in memory.
func Buffers(t Transformer) bool {
	_, ok := t.(bufferer)
	return ok
}

// MaxEncodedLen is the largest encoded input Untransform accepts from t for
// at most plainLen bytes of plaintext.
func MaxEncodedLen(t Transformer, plainLen int64) int64 {
	if st, ok := t.(SizedTransformer); ok {
		return st.EncodedLen(plainLen)
	}
	return plainLen + pdfEncodedSlack
}
