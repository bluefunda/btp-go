module github.com/bluefunda/btp-go/examples/http-odata

go 1.25.10

require (
	github.com/bluefunda/btp-go/binding v0.1.2
	github.com/bluefunda/btp-go/destination v0.2.0
	github.com/bluefunda/btp-go/httpclient v0.1.1
	github.com/bluefunda/btp-go/xsuaa v0.1.2
)

replace (
	github.com/bluefunda/btp-go/binding => ../../binding
	github.com/bluefunda/btp-go/destination => ../../destination
	github.com/bluefunda/btp-go/httpclient => ../../httpclient
	github.com/bluefunda/btp-go/xsuaa => ../../xsuaa
)
