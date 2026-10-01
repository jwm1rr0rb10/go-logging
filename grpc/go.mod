module github.com/jwm1rr0rb10/go-logging/grpc/v2

go 1.25.0

require (
	github.com/jwm1rr0rb10/go-logging/v2 v2.0.0
	google.golang.org/grpc v1.80.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260120221211-b8f7ae30c516 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// Local development: the root module is in the parent directory. Ignored by
// consumers, who get the tagged v2 release.
replace github.com/jwm1rr0rb10/go-logging/v2 => ../
