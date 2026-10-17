// The importer check: a separate module that uses koc's public API the way a
// downstream importer does, so a pkg/ package that only builds from inside
// this repository (an internal/ import, a vendored-only dependency) fails
// here. It needs its dependencies resolved, so it runs in supply-chain.yml,
// never in the offline ci.yml; the root module's ./... and vendor/ exclude it.
module github.com/ftarasenko/go-openstackclient/test/importer

go 1.26.0

require (
	github.com/ftarasenko/go-openstackclient v0.0.0
	github.com/gophercloud/gophercloud/v2 v2.15.0
)

require gopkg.in/yaml.v2 v2.4.0 // indirect

replace github.com/ftarasenko/go-openstackclient => ../..
