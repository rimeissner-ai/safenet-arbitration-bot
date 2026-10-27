package functions

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	token     = ethrpc.MustParseAddress("0x7000000000000000000000000000000000000001")
	alice     = ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000001")
	bob       = ethrpc.MustParseAddress("0xb0b0000000000000000000000000000000000002")
	singleton = ethrpc.MustParseAddress("0x41675C099F32341bf84BFc5382aF534df5C7461a")
)

// TestSelectors checks the selectors of the functions against those of deployed
// contracts.
func TestSelectors(t *testing.T) {
	for signature, want := range map[string]string{
		"transfer(address,uint256)":             "0xa9059cbb",
		"transferFrom(address,address,uint256)": "0x23b872dd",
		"approve(address,uint256)":              "0x095ea7b3",
		"execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)": "0x6a761202",
		"createProxyWithNonce(address,bytes,uint256)":                                                "0x1688f0b9",
	} {
		selector := solabi.Selector(signature)
		if got := ethrpc.Bytes(selector[:]).String(); got != want {
			t.Errorf("selector of %s: got %s, want %s", signature, got, want)
		}
		if _, ok := definitions[selector]; !ok {
			t.Errorf("%s isn't defined", signature)
		}
	}
}

// execTransaction returns the calldata of an execTransaction call of a Safe.
// Its two signatures start with transfer call data, which must not be decoded.
func execTransaction(to ethrpc.Address, value int64, data []byte, operation uint64) ethrpc.Bytes {
	signatures := make([]byte, 130)
	copy(signatures, solabi.Call(solabi.Selector("transfer(address,uint256)"), alice, big.NewInt(1)))
	return solabi.Call(solabi.Selector("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)"),
		to, big.NewInt(value), data, operation, new(big.Int), new(big.Int), new(big.Int), ethrpc.Address{}, ethrpc.Address{}, signatures)
}

func TestDecode(t *testing.T) {
	transfer := solabi.Call(solabi.Selector("transfer(address,uint256)"), bob, big.NewInt(7))
	dirtyAddress := append([]byte{}, transfer...)
	dirtyAddress[4] = 1
	nested := execTransaction(token, 0, transfer, 0)
	initializer := solabi.Call(solabi.Selector("setup()"))
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			"transfer", transfer,
			`{"name":"transfer","signature":"transfer(address,uint256)","args":[` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":7}]}`,
		},
		{
			"transfer with data after its arguments", append(append([]byte{}, transfer...), 0xff),
			`{"name":"transfer","signature":"transfer(address,uint256)","args":[` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":7}]}`,
		},
		{
			"transfer with upper bytes in its address", dirtyAddress,
			`{"name":"transfer","signature":"transfer(address,uint256)","args":[` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":7}]}`,
		},
		{
			"transferFrom", solabi.Call(solabi.Selector("transferFrom(address,address,uint256)"), alice, bob, big.NewInt(3)),
			`{"name":"transferFrom","signature":"transferFrom(address,address,uint256)","args":[` +
				`{"name":"from","type":"address","value":"0xa11ce00000000000000000000000000000000001"},` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":3}]}`,
		},
		{
			"approve", solabi.Call(solabi.Selector("approve(address,uint256)"), bob, big.NewInt(9)),
			`{"name":"approve","signature":"approve(address,uint256)","args":[` +
				`{"name":"spender","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":9}]}`,
		},
		{
			"createProxyWithNonce", solabi.Call(solabi.Selector("createProxyWithNonce(address,bytes,uint256)"), singleton, initializer, big.NewInt(4)),
			`{"name":"createProxyWithNonce","signature":"createProxyWithNonce(address,bytes,uint256)","args":[` +
				`{"name":"singleton","type":"address","value":"0x41675c099f32341bf84bfc5382af534df5c7461a"},` +
				`{"name":"initializer","type":"bytes","value":"0xba0bba40"},` +
				`{"name":"saltNonce","type":"uint256","value":4}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(Decode(test.data))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Errorf("Decode:\ngot  %s\nwant %s", got, test.want)
			}
		})
	}

	t.Run("execTransaction of a transfer", func(t *testing.T) {
		f := Decode(nested)
		if f == nil || f.Name != "execTransaction" || len(f.Args) != 10 {
			t.Fatalf("Decode: got %+v, want execTransaction with 10 arguments", f)
		}
		if to := f.Args[0]; to.Name != "to" || to.Value != token {
			t.Errorf("to: got %+v, want %s", to, token)
		}
		if op := f.Args[3]; op.Name != "operation" || op.Value != uint64(0) {
			t.Errorf("operation: got %+v, want 0", op)
		}
		data := f.Args[2]
		if data.Name != "data" || data.Call == nil || data.Call.Name != "transfer" {
			t.Fatalf("data: got %+v, want the decoded transfer", data)
		}
		if to := data.Call.Args[0]; to.Value != bob {
			t.Errorf("recipient of the inner transfer: got %v, want %s", to.Value, bob)
		}
		if sigs := f.Args[9]; sigs.Name != "signatures" || sigs.Call != nil || len(sigs.Value.(ethrpc.Bytes)) != 130 {
			t.Errorf("signatures: got %+v, want 130 bytes that aren't decoded as a call", sigs)
		}
		if got, want := f.String(), "execTransaction > transfer"; got != want {
			t.Errorf("String: got %q, want %q", got, want)
		}
	})

	t.Run("execTransaction of unknown data", func(t *testing.T) {
		f := Decode(execTransaction(token, 0, []byte{1, 2, 3, 4, 5}, 1))
		if f == nil || f.Args[2].Call != nil || f.String() != "execTransaction" {
			t.Errorf("Decode: got %+v, want execTransaction whose data isn't decoded", f)
		}
	})
}

func TestDecodeMismatches(t *testing.T) {
	transfer := solabi.Call(solabi.Selector("transfer(address,uint256)"), bob, big.NewInt(7))
	badOperation := execTransaction(token, 0, nil, 0)
	badOperation[4+3*32+30] = 1 // operation 256
	badOffset := execTransaction(token, 0, nil, 0)
	badOffset[4+2*32+28] = 0xff // past the end of the data
	for name, data := range map[string][]byte{
		"no data":          nil,
		"short selector":   transfer[:3],
		"unknown selector": solabi.Call(solabi.Selector("transfer(address,uint128)"), bob, big.NewInt(7)),
		"short arguments":  transfer[:4+40],
		"operation 256":    badOperation,
		"bad offset":       badOffset,
	} {
		if f := Decode(data); f != nil {
			t.Errorf("%s: Decode got %+v, want nil", name, f)
		}
	}
}

func TestDecodeDepth(t *testing.T) {
	data := solabi.Call(solabi.Selector("transfer(address,uint256)"), bob, big.NewInt(1))
	for range maxDepth + 2 {
		data = execTransaction(token, 0, data, 0)
	}
	depth := 0
	for f := Decode(data); f != nil; f = f.Args[2].Call {
		depth++
		if f.Name != "execTransaction" {
			break
		}
	}
	if depth != maxDepth {
		t.Errorf("decoded %d levels, want %d", depth, maxDepth)
	}
}
