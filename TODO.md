# Parameter obfuscation
I would like to provide an obfuscation mechanism for parameters (usually sent in the form of JSON bodies). The idea is to use the auth key as shared secret and derive a key from it. Using this key, the entire parameter blob is encryptes and sent as a single form post (base64 encoded). The backend then retrieves the blob, decods and decrypts it and parses the JSON exactly as before. Please review this idea. Do you see any major issues?

# Later: labeled key derivation for X-Auth
The parameter-obfuscation encryption key uses a labeled derivation from the raw auth key
(e.g. `SHA256(key || ":obfuscate")`) instead of reusing/relating to the X-Auth token's
1000x-iterated SHA-256. Apply the same labeled-derivation treatment to the X-Auth token
itself (`internal/authhash.Hash`), so it's no longer just "iteration 0 of a shared chain"
but a distinct, explicitly domain-separated value.

