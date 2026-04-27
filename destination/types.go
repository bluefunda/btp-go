package destination

// Destination holds the resolved fields of a BTP Destination Service entry.
type Destination struct {
	// Name is the logical destination name as configured in the BTP cockpit.
	Name string

	// Type is the destination type: "HTTP", "TCP", "MAIL", "RFC", or "LDAP".
	Type string

	// ProxyType describes connectivity: "Internet", "OnPremise", or "PrivateLink".
	ProxyType string

	// Authentication is the authentication method configured on the destination.
	Authentication string

	// URL is populated for HTTP destinations.
	URL string

	// Host is populated for TCP (and on-prem) destinations.
	Host string

	// Port is populated for TCP destinations (string to preserve leading zeros
	// and avoid uint16 overflow at parse time).
	Port string

	// User is the username for authentication (SSH/FTP destinations).
	User string

	// Password is the credential for authentication (SSH/FTP destinations).
	Password string

	// Path is the remote path configured on the destination (e.g. SFTP root dir).
	Path string

	// CloudConnectorLocationID holds the SCC location the destination routes
	// through. Empty string means the default location.
	CloudConnectorLocationID string

	// Properties contains all remaining key/value pairs from the
	// destinationConfiguration map that are not mapped to named fields above.
	// Consumers use this for service-specific keys such as "User", "sshKey",
	// "RemotePath", etc.
	Properties map[string]string

	// AuthTokens holds the OAuth/SAML tokens that the Destination Service may
	// return alongside the destination configuration.
	AuthTokens []AuthToken
}

// AuthToken represents a single authentication token returned by the
// Destination Service alongside a resolved destination.
type AuthToken struct {
	// Type is the token type, e.g. "Bearer".
	Type string `json:"type"`

	// Value is the raw token string.
	Value string `json:"value"`

	// HTTPHeader holds the header key and value that should be sent with
	// requests to the target system.
	HTTPHeader struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"http_header"`

	// ExpiresIn is the remaining token lifetime in seconds.
	ExpiresIn int `json:"expires_in,string"`

	// Error is non-empty when the Destination Service could not obtain a
	// token for this destination.
	Error string `json:"error"`
}
