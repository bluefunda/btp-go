module github.com/bluefunda/btp-go/examples/sftp-count

go 1.25

require (
	github.com/bluefunda/btp-go/binding v0.0.0-00010101000000-000000000000
	github.com/bluefunda/btp-go/connectivity v0.0.0-00010101000000-000000000000
	github.com/bluefunda/btp-go/destination v0.0.0-00010101000000-000000000000
	github.com/bluefunda/btp-go/xsuaa v0.0.0-00010101000000-000000000000
	github.com/pkg/sftp v1.13.7
	golang.org/x/crypto v0.37.0
)

replace (
	github.com/bluefunda/btp-go/binding => ../../binding
	github.com/bluefunda/btp-go/connectivity => ../../connectivity
	github.com/bluefunda/btp-go/destination => ../../destination
	github.com/bluefunda/btp-go/xsuaa => ../../xsuaa
)
