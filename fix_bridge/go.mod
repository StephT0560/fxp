module fix_bridge

go 1.25.0

toolchain go1.26.10

require (
	go_gateway v0.0.0
	google.golang.org/protobuf v1.36.12
)

replace go_gateway => ../go_gateway
