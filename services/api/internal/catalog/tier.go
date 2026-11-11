package catalog

import "strings"

// Interpretation tiers (ADR 0055): how far an interpretation of the data (an orientation declaration, a unit, the columns of a
// catalog, a mask's type) is established. They are a vocabulary of the app's own knowledge, not of the file's: the worker's
// header fields "bunit_status" and the like keep their own words ("declared" there means the file says it).
const (
	// TierVerified: established by a check the app ran and that passed (a BUNIT card present and parsed; a mask of the pixels with
	// no value, computed from the data). Nothing is verified by being stored.
	TierVerified = "verified"
	// TierDeclared: the researcher said so.
	TierDeclared = "declared"
	// TierInferred: proposed by an adapter or a rule, named with the version of its mapping; not confirmed by anyone.
	TierInferred = "inferred"
	// TierUnresolved: nothing is known.
	TierUnresolved = "unresolved"
)

// adapterMappings is the version of each adapter's mapping from what it read to an interpretation. A rule whose mapping changes
// gets a new version here, so a stored proposal says which mapping made it. Rows that existed before versions did were made by
// the first mapping, "1".
var adapterMappings = map[string]string{
	"vgt-run-adapter":   "1",
	"rohsa-run-adapter": "1",
	"vesqira-analysis":  "1",
}

// RuleAuthorPrefix starts the author of a declaration a researcher's own import rule made ("rule:<name>", ADR 0058). The
// discover package spells the same prefix; a test in the server package keeps the two equal.
const RuleAuthorPrefix = "rule:"

// TierOf is the tier and mapping version of an interpretation written by `by`: the researcher ("user" or empty) declared it, and
// so did their import rule ("rule:<name>": the researcher wrote the rule and chose what it applies to, so the declaration is
// theirs, and the author keeps the rule's name); anything else is an adapter, so it is inferred, with its mapping version.
func TierOf(by string) (tier, mapping string) {
	if by == "" || by == "user" || strings.HasPrefix(by, RuleAuthorPrefix) {
		return TierDeclared, ""
	}
	if v, ok := adapterMappings[by]; ok {
		return TierInferred, v
	}
	return TierInferred, "1"
}

// ValidTier reports whether t is one of the four tiers.
func ValidTier(t string) bool {
	return t == TierVerified || t == TierDeclared || t == TierInferred || t == TierUnresolved
}
