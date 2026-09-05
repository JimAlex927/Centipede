package http

import (
	"encoding/json"
	"strings"

	"github.com/crewjam/saml"
)

// ssoClaimValues accepts the two shapes used by identity providers for group
// claims: a single string and an array of strings.
func ssoClaimValues(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		return values
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return []string{value}
	}
	return nil
}

func normalizeSSOGroupNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 255 {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
		if len(result) == 100 {
			break
		}
	}
	return result
}

func ssoAttributeIsGroup(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "memberof" || name == "groups" || name == "roles" ||
		strings.HasSuffix(name, "/memberof") || strings.HasSuffix(name, "/groups") || strings.HasSuffix(name, "/roles")
}

func samlIdentityWithGroups(assertion *saml.Assertion) (providerUserID, email, name string, groups []string) {
	if assertion == nil {
		return "", "", "", nil
	}
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		providerUserID = strings.TrimSpace(assertion.Subject.NameID.Value)
	}
	attributes := make(map[string]string)
	for _, statement := range assertion.AttributeStatements {
		for _, attribute := range statement.Attributes {
			attributeName := strings.ToLower(strings.TrimSpace(attribute.Name))
			friendlyName := strings.ToLower(strings.TrimSpace(attribute.FriendlyName))
			for index, attributeValue := range attribute.Values {
				value := strings.TrimSpace(attributeValue.Value)
				if value == "" {
					continue
				}
				if index == 0 && attributeName != "" {
					attributes[attributeName] = value
				}
				if index == 0 && friendlyName != "" {
					attributes[friendlyName] = value
				}
				if ssoAttributeIsGroup(attributeName) || ssoAttributeIsGroup(friendlyName) {
					groups = append(groups, value)
				}
			}
		}
	}
	email = firstIdentityValue(attributes, "email", "mail", "emailaddress", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn")
	name = firstIdentityValue(attributes, "name", "displayname", "cn", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name")
	if providerUserID == "" {
		providerUserID = email
	}
	return providerUserID, email, name, normalizeSSOGroupNames(groups)
}
