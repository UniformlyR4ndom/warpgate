package transform

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

type aesCTRArgs struct {
	Key string `json:"key"`
	IV  string `json:"iv"`
}

type aesCTRTransformer struct {
	key []byte
	iv  []byte
}

func (t *aesCTRTransformer) Transform(r io.Reader) (io.Reader, error) {
	block, err := aes.NewCipher(t.key)
	if err != nil {
		return nil, fmt.Errorf("aes-ctr cipher: %w", err)
	}
	stream := cipher.NewCTR(block, t.iv)
	return &cipher.StreamReader{S: stream, R: r}, nil
}

func (t *aesCTRTransformer) Untransform(r io.Reader) (io.Reader, error) {
	return t.Transform(r)
}

func newAESCTRTransformer(raw json.RawMessage) (Transformer, error) {
	var args aesCTRArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("aes-ctr args: %w", err)
	}
	key, err := hex.DecodeString(args.Key)
	if err != nil {
		return nil, fmt.Errorf("aes-ctr key: %w", err)
	}
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return nil, fmt.Errorf("aes-ctr key must be 16, 24, or 32 bytes (got %d)", len(key))
	}
	iv, err := hex.DecodeString(args.IV)
	if err != nil {
		return nil, fmt.Errorf("aes-ctr iv: %w", err)
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("aes-ctr iv must be %d bytes (got %d)", aes.BlockSize, len(iv))
	}
	return &aesCTRTransformer{key: key, iv: iv}, nil
}
