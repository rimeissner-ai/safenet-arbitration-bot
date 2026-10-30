package solabi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ABIEntry is an entry of a contract's JSON ABI, as the Solidity compiler
// writes it: a function, the fallback or receive function, a constructor, an
// event, or an error.
type ABIEntry struct {
	Type            string     `json:"type"`
	Name            string     `json:"name"`
	Inputs          []ABIParam `json:"inputs"`
	StateMutability string     `json:"stateMutability"`
	// Payable and Constant are the state mutability of an entry of a compiler
	// before Solidity 0.6, which has no StateMutability.
	Payable  bool `json:"payable"`
	Constant bool `json:"constant"`
}

// ABIParam is a parameter of an ABIEntry.
type ABIParam struct {
	// Type is the parameter's canonical type, such as "uint256" or "address[]",
	// where a tuple is "tuple", with array suffixes if it is an array of tuples.
	Type string `json:"type"`
	// Components are the types of a tuple's fields.
	Components []ABIParam `json:"components"`
}

// Mutability returns the state mutability of the entry: "pure", "view",
// "nonpayable", or "payable".
func (e ABIEntry) Mutability() string {
	switch {
	case e.StateMutability != "":
		return e.StateMutability
	case e.Payable:
		return "payable"
	case e.Constant:
		return "view"
	}
	return "nonpayable"
}

var (
	identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	// typePattern splits a type into its base type, its size, and its array
	// suffixes.
	typePattern = regexp.MustCompile(`^([a-z]+)([0-9x]*)((?:\[[0-9]*\])*)$`)
)

// Signature returns the canonical signature of the entry, its name followed by
// the canonical types of its inputs, such as "transfer(address,uint256)", from
// which Selector computes a function's selector. It fails if the name or a type
// isn't valid Solidity, so a signature never has other characters.
func (e ABIEntry) Signature() (string, error) {
	if !identifier.MatchString(e.Name) {
		return "", fmt.Errorf("invalid name %q", e.Name)
	}
	types, err := tupleType(e.Inputs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", e.Name, err)
	}
	return e.Name + types, nil
}

// tupleType returns the canonical type of a tuple of params, such as
// "(address,uint256)".
func tupleType(params []ABIParam) (string, error) {
	types := make([]string, len(params))
	for i, p := range params {
		t, err := p.canonicalType()
		if err != nil {
			return "", err
		}
		types[i] = t
	}
	return "(" + strings.Join(types, ",") + ")", nil
}

func (p ABIParam) canonicalType() (string, error) {
	m := typePattern.FindStringSubmatch(p.Type)
	if m == nil {
		return "", fmt.Errorf("invalid type %q", p.Type)
	}
	base, size, arrays := m[1], m[2], m[3]
	if base == "tuple" && size == "" {
		components, err := tupleType(p.Components)
		if err != nil {
			return "", err
		}
		return components + arrays, nil
	}
	if !validBase(base, size) {
		return "", fmt.Errorf("invalid type %q", p.Type)
	}
	return p.Type, nil
}

// validBase reports whether base with size, such as "uint" and "256", is an
// elementary Solidity type, as the canonical types of the ABI spell them.
func validBase(base, size string) bool {
	switch base {
	case "address", "bool", "string", "function":
		return size == ""
	case "bytes":
		n, err := strconv.Atoi(size)
		return size == "" || (err == nil && size[0] != '0' && n >= 1 && n <= 32)
	case "uint", "int":
		n, err := strconv.Atoi(size)
		return err == nil && size[0] != '0' && n >= 8 && n <= 256 && n%8 == 0
	case "ufixed", "fixed":
		m, n, ok := strings.Cut(size, "x")
		bits, err1 := strconv.Atoi(m)
		decimals, err2 := strconv.Atoi(n)
		return ok && err1 == nil && err2 == nil && m[0] != '0' && (n == "0" || n[0] != '0') &&
			bits >= 8 && bits <= 256 && bits%8 == 0 && decimals >= 0 && decimals <= 80
	}
	return false
}
