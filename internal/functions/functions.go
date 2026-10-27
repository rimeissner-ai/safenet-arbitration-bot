// Package functions decodes the call data of well-known functions: those of
// ERC-20 tokens, the execTransaction of Safe 1.3.0, 1.4.1, and 1.5.0, and the
// createProxyWithNonce of their proxy factories.
//
// Call data is decoded by its selector and ABI layout only, so data that
// decodes as one of them says nothing about the contract that it calls, whose
// code decides what the call does, or whether it does anything at all. Callers
// must look at the account called.
package functions

import (
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// Function is decoded call data.
type Function struct {
	Name string `json:"name"`
	// Signature is the canonical signature of the function, such as
	// "transfer(address,uint256)", whose selector the data starts with.
	Signature string `json:"signature"`
	Args      []Arg  `json:"args"`
}

// Arg is an argument of a function.
type Arg struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Value is an address, a *big.Int, a uint64, or bytes, all of which encode in
	// JSON as text or numbers.
	Value any `json:"value"`
	// Call is the decoded call data of a bytes argument that is the data of a call
	// that the function makes, such as the data of execTransaction, if it is that
	// of one of the well-known functions. It is left out otherwise.
	Call *Function `json:"call,omitempty"`
}

// param is a parameter of a function. A bytes parameter that is call is the
// data of a call that the function makes.
type param struct {
	name string
	typ  string
	call bool
}

// definition is a well-known function: its name and parameters, in order.
type definition struct {
	name      string
	signature string
	params    []param
}

// definitions are the functions that Decode knows, by their selector.
var definitions = map[[4]byte]definition{}

// define adds the function with the given name and parameters.
func define(name string, params ...param) {
	types := make([]string, len(params))
	for i, p := range params {
		types[i] = p.typ
	}
	signature := name + "(" + strings.Join(types, ",") + ")"
	selector := solabi.Selector(signature)
	if _, ok := definitions[selector]; ok {
		panic("functions: two definitions with the selector of " + signature)
	}
	definitions[selector] = definition{name, signature, params}
}

// p is a parameter, and data a bytes parameter that is call data.
func p(typ, name string) param { return param{name: name, typ: typ} }
func data(name string) param   { return param{name: name, typ: "bytes", call: true} }

func init() {
	// ERC-20.
	define("transfer", p("address", "to"), p("uint256", "value"))
	define("transferFrom", p("address", "from"), p("address", "to"), p("uint256", "value"))
	define("approve", p("address", "spender"), p("uint256", "value"))

	// Safe. It runs the call that data encodes on to, with value and operation, if
	// the signatures are valid for its owners.
	define("execTransaction", p("address", "to"), p("uint256", "value"), data("data"), p("uint8", "operation"),
		p("uint256", "safeTxGas"), p("uint256", "baseGas"), p("uint256", "gasPrice"), p("address", "gasToken"),
		p("address", "refundReceiver"), p("bytes", "signatures"))

	// Safe proxy factories. They deploy a proxy of singleton, and call it with the
	// initializer.
	define("createProxyWithNonce", p("address", "singleton"), p("bytes", "initializer"), p("uint256", "saltNonce"))
}

// maxDepth is how many levels of nested call data Decode decodes.
const maxDepth = 8

// Decode decodes call data as one of the well-known functions, and the data of
// the calls that it makes as well-known functions, if they are. It returns nil
// if the data isn't one, or has a well-known selector but arguments that don't
// fit its ABI layout. Data after the arguments is ignored, as the Solidity ABI
// decoder ignores it.
func Decode(calldata []byte) *Function {
	return decode(calldata, 0)
}

func decode(calldata []byte, depth int) *Function {
	if len(calldata) < 4 || depth >= maxDepth {
		return nil
	}
	def, ok := definitions[[4]byte(calldata[:4])]
	if !ok {
		return nil
	}
	d := solabi.NewDecoder(calldata[4:])
	args := make([]Arg, len(def.params))
	for i, p := range def.params {
		arg := Arg{Name: p.name, Type: p.typ}
		switch p.typ {
		case "address":
			// The Safe and contracts built with Solidity's ABI coder v1, such as WETH9,
			// take an address from the low 20 bytes of its word, ignoring the others.
			w := d.Bytes32(i)
			arg.Value = ethrpc.Address(w[12:])
		case "uint256":
			arg.Value = d.Uint(i)
		case "uint8":
			v := d.Uint64(i)
			if v > 255 {
				return nil
			}
			arg.Value = v
		case "bytes":
			b := d.Bytes(i)
			arg.Value = ethrpc.Bytes(b)
			if p.call && d.Err() == nil {
				arg.Call = decode(b, depth+1)
			}
		}
		if d.Err() != nil {
			return nil
		}
		args[i] = arg
	}
	return &Function{Name: def.name, Signature: def.signature, Args: args}
}

// String returns the name of the function, followed by those of the well-known
// functions of the calls that it makes, such as "execTransaction > transfer".
func (f *Function) String() string {
	names := []string{f.Name}
	for _, a := range f.Args {
		if a.Call != nil {
			names = append(names, a.Call.String())
		}
	}
	return strings.Join(names, " > ")
}
