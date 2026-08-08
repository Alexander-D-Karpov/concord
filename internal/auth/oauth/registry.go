package oauth

// FieldMap names the JSON fields in a provider's user-info response that carry the
// stable subject id, email, display name, and avatar URL. Values are read
// case-sensitively and coerced to strings, so numeric ids (e.g. GitHub) work too.
type FieldMap struct {
	ID      string
	Email   string
	Name    string
	Picture string
}

// ProviderDef is the static catalog entry for one OAuth provider: everything about
// it that is not a per-deployment secret. Adding support for a new provider is a
// matter of appending one entry to Registry and setting its
// <EnvPrefix>_CLIENT_ID / _CLIENT_SECRET / _REDIRECT_URL environment variables.
type ProviderDef struct {
	Name        string   // canonical id, e.g. "google"
	EnvPrefix   string   // env var prefix, e.g. "OAUTH_GOOGLE"
	DisplayName string   // human label, e.g. "Google"
	Icon        string   // client icon hint, e.g. "google"
	Issuer      string   // OIDC issuer for discovery-based availability checks; "" skips validation
	AuthURL     string   // authorization endpoint
	TokenURL    string   // token endpoint
	UserInfoURL string   // user-info endpoint
	Scopes      []string // scopes requested at authorization
	Fields      FieldMap // user-info field mapping
}

// Credentials are the per-deployment secrets for a provider, read from the
// environment by the config package. RedirectURLs is the exact-match allowlist of
// permitted redirect URIs; loopback redirects are always permitted in addition,
// per RFC 8252, so a desktop app can use an ephemeral 127.0.0.1 port.
type Credentials struct {
	ClientID     string
	ClientSecret string
	RedirectURLs []string
}

// Registry is the catalog of known OAuth providers. Only entries whose credentials
// are configured (see config.loadOAuthProviders) are instantiated at runtime.
//
// To add a provider: append one entry here and set its env vars. If the provider
// is OIDC, set Issuer so availability is validated against its discovery document;
// otherwise leave Issuer empty and it is treated as available on credential
// presence alone.
var Registry = []ProviderDef{
	{
		Name:        "google",
		EnvPrefix:   "OAUTH_GOOGLE",
		DisplayName: "Google",
		Icon:        "google",
		Issuer:      "https://accounts.google.com",
		AuthURL:     "https://accounts.google.com/o/oauth2/auth",
		TokenURL:    "https://oauth2.googleapis.com/token",
		UserInfoURL: "https://www.googleapis.com/oauth2/v2/userinfo",
		Scopes: []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Fields: FieldMap{ID: "id", Email: "email", Name: "name", Picture: "picture"},
	},
}

// Definition returns the catalog entry for the named provider.
func Definition(name string) (ProviderDef, bool) {
	for _, d := range Registry {
		if d.Name == name {
			return d, true
		}
	}
	return ProviderDef{}, false
}

// EnvKeys returns the environment variable names this provider reads for its
// client id, client secret, and redirect-URL allowlist.
func (d ProviderDef) EnvKeys() (clientID, clientSecret, redirectURL string) {
	return d.EnvPrefix + "_CLIENT_ID", d.EnvPrefix + "_CLIENT_SECRET", d.EnvPrefix + "_REDIRECT_URL"
}
