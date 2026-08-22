package operation_setting

import "strings"

// DemoSiteEnabled is a flag to enable/disable the demo site mode.
var DemoSiteEnabled = false

// SelfUseModeEnabled is a flag to enable/disable self-use mode.
var SelfUseModeEnabled = false

var AutomaticDisableKeywords = []string{
	"Your credit balance is too low",
	"This organization has been disabled.",
	"You exceeded your current quota",
	"Permission denied",
	"The security token included in the request is invalid",
	"Operation not allowed",
	"Your account is not authorized",
}

// AutomaticDisableKeywordsToString converts the automatic disable keywords to a string.
func AutomaticDisableKeywordsToString() string {
	return strings.Join(AutomaticDisableKeywords, "\n")
}

// AutomaticDisableKeywordsFromString parses automatic disable keywords from a string.
func AutomaticDisableKeywordsFromString(s string) {
	AutomaticDisableKeywords = []string{}
	ak := strings.Split(s, "\n")
	for _, k := range ak {
		k = strings.TrimSpace(k)
		k = strings.ToLower(k)
		if k != "" {
			AutomaticDisableKeywords = append(AutomaticDisableKeywords, k)
		}
	}
}
