# Trade-capture wire vectors

The Go vector is generated with protobuf-go and the Rust vector with prost.
Both generators use the same fixed public trade report. Keep the vectors
independent: normal tests compare each implementation against the other
implementation's checked-in bytes.

From the repository root, generate or update both vectors with:

```sh
mkdir -p tests/vectors
(cd go_gateway && FXP_UPDATE_VECTORS=1 go test ./... -run '^TestTradeVectorGenerate$')
(cd fxp_core && FXP_UPDATE_VECTORS=1 cargo test trade_vector_generate)
```
