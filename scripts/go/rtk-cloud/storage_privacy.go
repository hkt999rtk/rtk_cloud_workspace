package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// validateStoragePrivacy only reads configuration. A limited access key proves
// the caller's access, not the absence of public access through another grant.
func (c deploymentCredentialChecker) validateStoragePrivacy(store provisionObjectStore) error {
	body, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", url.Values{"acl": {""}}, nil)
	if err != nil {
		return fmt.Errorf("bucket privacy requires readable ACL: %w", err)
	}
	if err := validatePrivateStorageACL(body); err != nil {
		return err
	}
	body, err = provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", url.Values{"policy": {""}}, nil)
	if err != nil {
		var response *provisionObjectStorageHTTPError
		if errors.As(err, &response) && response.StatusCode == http.StatusNotFound {
			return nil // No bucket policy; the ACL above still has to be private.
		}
		return fmt.Errorf("bucket privacy requires readable policy: %w", err)
	}
	return validatePrivateStoragePolicy(body)
}

func validatePrivateStorageACL(body []byte) error {
	var acl struct {
		XMLName xml.Name            `xml:"AccessControlPolicy"`
		Owner   struct{ ID string } `xml:"Owner"`
		List    *struct {
			Grants []struct {
				Grantee struct {
					Type string `xml:"type,attr"`
					ID   string
					URI  string
				} `xml:"Grantee"`
				Permission string
			} `xml:"Grant"`
		} `xml:"AccessControlList"`
	}
	if xml.Unmarshal(body, &acl) != nil || acl.Owner.ID == "" || acl.List == nil {
		return errors.New("bucket privacy could not validate ACL")
	}
	for _, grant := range acl.List.Grants {
		// Canonical-user grants are explicit private identities. Group grants
		// include both anonymous and any-authenticated-account access.
		if grant.Grantee.URI != "" || (grant.Grantee.Type != "" && grant.Grantee.Type != "CanonicalUser") || !fixedStoragePrincipal(grant.Grantee.ID) || grant.Permission == "" {
			return errors.New("bucket ACL is public or has an unsupported grantee")
		}
	}
	return nil
}

func validatePrivateStoragePolicy(body []byte) error {
	var policy struct{ Statement json.RawMessage }
	if json.Unmarshal(body, &policy) != nil || len(policy.Statement) == 0 || strings.TrimSpace(string(policy.Statement)) == "null" {
		return errors.New("bucket privacy could not validate policy")
	}
	type statement struct {
		Effect       string
		Principal    json.RawMessage
		NotPrincipal json.RawMessage
	}
	var statements []statement
	if json.Unmarshal(policy.Statement, &statements) != nil {
		var single statement
		if json.Unmarshal(policy.Statement, &single) != nil {
			return errors.New("bucket privacy could not validate policy statements")
		}
		statements = []statement{single}
	}
	for _, item := range statements {
		if item.Effect == "Deny" {
			continue
		}
		if item.Effect != "Allow" || len(item.NotPrincipal) != 0 || !privateStoragePrincipal(item.Principal) {
			return errors.New("bucket policy permits public access or has an unsupported principal")
		}
	}
	return nil
}

func fixedStoragePrincipal(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "*?")
}

func privateStoragePrincipal(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return fixedStoragePrincipal(value)
	}
	var principals map[string]json.RawMessage
	if json.Unmarshal(raw, &principals) != nil || len(principals) == 0 {
		return false
	}
	for kind, principal := range principals {
		if kind != "AWS" && kind != "CanonicalUser" && kind != "Service" {
			return false
		}
		if json.Unmarshal(principal, &value) == nil {
			if !fixedStoragePrincipal(value) {
				return false
			}
			continue
		}
		var values []string
		if json.Unmarshal(principal, &values) != nil || len(values) == 0 {
			return false
		}
		for _, value := range values {
			if !fixedStoragePrincipal(value) {
				return false
			}
		}
	}
	return true
}
