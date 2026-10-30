package solabi

import (
	"encoding/json"
	"testing"
)

func TestSignature(t *testing.T) {
	for _, test := range []struct {
		entry string
		want  string
	}{
		{`{"type": "function", "name": "transfer", "inputs": [{"type": "address"}, {"type": "uint256"}]}`, "transfer(address,uint256)"},
		{`{"type": "function", "name": "nonce", "inputs": []}`, "nonce()"},
		{`{"type": "function", "name": "f", "inputs": [
			{"type": "tuple[2][]", "components": [{"type": "bytes32"}, {"type": "tuple", "components": [{"type": "bool"}, {"type": "string"}]}]},
			{"type": "bytes"}, {"type": "fixed128x18"}, {"type": "ufixed8x0"}, {"type": "function"}, {"type": "int8[3]"}
		]}`, "f((bytes32,(bool,string))[2][],bytes,fixed128x18,ufixed8x0,function,int8[3])"},
		{`{"type": "function", "name": "$_x9", "inputs": [{"type": "tuple", "components": []}]}`, "$_x9(())"},
	} {
		var entry ABIEntry
		if err := json.Unmarshal([]byte(test.entry), &entry); err != nil {
			t.Fatal(err)
		}
		if got, err := entry.Signature(); got != test.want || err != nil {
			t.Errorf("Signature(%s) = %q, %v; want %q", test.entry, got, err, test.want)
		}
	}
}

func TestSignatureInvalid(t *testing.T) {
	for _, entry := range []ABIEntry{
		{Name: ""},
		{Name: "9f"},
		{Name: "f\x1b[31m"},
		{Name: "f(uint256)"},
		{Name: "f", Inputs: []ABIParam{{Type: "uint"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "uint7"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "uint264"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "uint08"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "bytes0"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "bytes33"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "address20"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "fixed"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "fixed128x81"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "uint256[-1]"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "contract Token"}}},
		{Name: "f", Inputs: []ABIParam{{Type: "tuple", Components: []ABIParam{{Type: "uint"}}}}},
	} {
		if got, err := entry.Signature(); err == nil {
			t.Errorf("Signature(%+v) = %q, want an error", entry, got)
		}
	}
}

func TestMutability(t *testing.T) {
	for _, test := range []struct {
		entry ABIEntry
		want  string
	}{
		{ABIEntry{StateMutability: "view"}, "view"},
		{ABIEntry{StateMutability: "payable"}, "payable"},
		{ABIEntry{Payable: true}, "payable"},
		{ABIEntry{Constant: true}, "view"},
		{ABIEntry{}, "nonpayable"},
	} {
		if got := test.entry.Mutability(); got != test.want {
			t.Errorf("Mutability(%+v) = %q, want %q", test.entry, got, test.want)
		}
	}
}
