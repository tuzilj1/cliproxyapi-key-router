module github.com/tuzilj1/cliproxyapi-key-router

go 1.26.0

require (
	github.com/router-for-me/CLIProxyAPI/v8 v8.0.0
	gopkg.in/yaml.v3 v3.0.1
)

// Build against the CLIProxyAPI source checkout matching the deployed image.
replace github.com/router-for-me/CLIProxyAPI/v8 => ../CLIProxyAPI
