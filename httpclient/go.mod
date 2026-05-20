module github.com/bluefunda/btp-go/httpclient

go 1.25.10

require (
	github.com/bluefunda/btp-go/binding v0.1.2
	github.com/bluefunda/btp-go/connectivity v0.1.2
	github.com/bluefunda/btp-go/destination v0.2.0
	github.com/bluefunda/btp-go/xsuaa v0.1.2
)

replace (
	github.com/bluefunda/btp-go/binding => ../binding
	github.com/bluefunda/btp-go/connectivity => ../connectivity
	github.com/bluefunda/btp-go/destination => ../destination
	github.com/bluefunda/btp-go/xsuaa => ../xsuaa
)
