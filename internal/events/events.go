// Package events decodes the logs of well-known events: those of ERC-20 and
// ERC-721 tokens, WETH, and Safe 1.3.0, 1.4.1, and 1.5.0 and their proxy
// factories.
//
// Logs are decoded by their signature only, so any contract can emit a log that
// decodes as one of them. Callers must look at the emitter.
package events

import (
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// Event is a decoded log.
type Event struct {
	Name string `json:"name"`
	// Signature is the canonical signature of the event, such as
	// "Transfer(address,address,uint256)". Whether its parameters are indexed isn't
	// part of it, so the same signature is used for the versions of an event that
	// differ in it.
	Signature string `json:"signature"`
	Args      []Arg  `json:"args"`
}

// Arg is an argument of an event.
type Arg struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Value is an address, a hash, a *big.Int, a bool, or bytes, all of which
	// encode in JSON as text or numbers.
	Value any `json:"value"`
}

// param is a parameter of an event.
type param struct {
	name    string
	typ     string
	indexed bool
}

// definition is a layout of an event: its name, and its parameters, in order.
type definition struct {
	name      string
	signature string
	params    []param
}

// definitions are the layouts of the events that Decode knows, by their topic.
// Several layouts can share a topic when the indexing of a parameter differs
// between token standards or Safe versions.
var definitions = map[ethrpc.Hash][]definition{}

// define adds a layout for the event with the given name and parameters.
func define(name string, params ...param) {
	types := make([]string, len(params))
	for i, p := range params {
		types[i] = p.typ
	}
	signature := name + "(" + strings.Join(types, ",") + ")"
	topic := solabi.Event(signature)
	definitions[topic] = append(definitions[topic], definition{name, signature, params})
}

// in and ix are a parameter in the data of a log, and an indexed one in its
// topics.
func in(typ, name string) param { return param{name, typ, false} }
func ix(typ, name string) param { return param{name, typ, true} }

// either defines the event with the single parameter, in each of its layouts,
// since some Safe versions index it and some don't.
func either(name, typ, param string) {
	define(name, in(typ, param))
	define(name, ix(typ, param))
}

func init() {
	// ERC-20 and ERC-721. Their Transfer and Approval events have the same
	// signatures, but ERC-721 indexes the token ID.
	define("Transfer", ix("address", "from"), ix("address", "to"), in("uint256", "value"))
	define("Transfer", ix("address", "from"), ix("address", "to"), ix("uint256", "tokenId"))
	define("Approval", ix("address", "owner"), ix("address", "spender"), in("uint256", "value"))
	define("Approval", ix("address", "owner"), ix("address", "approved"), ix("uint256", "tokenId"))
	define("ApprovalForAll", ix("address", "owner"), ix("address", "operator"), in("bool", "approved"))
	// WETH9.
	define("Deposit", ix("address", "dst"), in("uint256", "wad"))
	define("Withdrawal", ix("address", "src"), in("uint256", "wad"))

	// Safe. Version 1.3.0 doesn't index the hash of ExecutionSuccess and
	// ExecutionFailure, and the later versions do.
	for _, name := range []string{"ExecutionSuccess", "ExecutionFailure"} {
		define(name, in("bytes32", "txHash"), in("uint256", "payment"))
		define(name, ix("bytes32", "txHash"), in("uint256", "payment"))
	}
	// SafeL2 logs these before it executes a transaction.
	define("SafeMultiSigTransaction", in("address", "to"), in("uint256", "value"), in("bytes", "data"), in("uint8", "operation"),
		in("uint256", "safeTxGas"), in("uint256", "baseGas"), in("uint256", "gasPrice"), in("address", "gasToken"),
		in("address", "refundReceiver"), in("bytes", "signatures"), in("bytes", "additionalInfo"))
	define("SafeModuleTransaction", in("address", "module"), in("address", "to"), in("uint256", "value"), in("bytes", "data"), in("uint8", "operation"))
	define("ExecutionFromModuleSuccess", ix("address", "module"))
	define("ExecutionFromModuleFailure", ix("address", "module"))
	either("AddedOwner", "address", "owner")
	either("RemovedOwner", "address", "owner")
	either("ChangedThreshold", "uint256", "threshold")
	either("EnabledModule", "address", "module")
	either("DisabledModule", "address", "module")
	either("ChangedFallbackHandler", "address", "handler")
	either("ChangedGuard", "address", "guard")
	either("ChangedModuleGuard", "address", "moduleGuard")
	define("SafeReceived", ix("address", "sender"), in("uint256", "value"))
	define("ApproveHash", ix("bytes32", "approvedHash"), ix("address", "owner"))
	define("SignMsg", ix("bytes32", "msgHash"))

	// Safe proxy factories, when they create a proxy of a singleton. Version 1.3.0
	// doesn't index the proxy, and the later versions do.
	define("ProxyCreation", in("address", "proxy"), in("address", "singleton"))
	define("ProxyCreation", ix("address", "proxy"), in("address", "singleton"))
}

// Decode decodes a log as one of the well-known events. It returns nil if it
// isn't one, or if it has a well-known signature but a layout that doesn't fit
// any definition of it.
func Decode(log ethrpc.Log) *Event {
	if len(log.Topics) == 0 {
		return nil
	}
	for _, def := range definitions[log.Topics[0]] {
		if args, ok := def.decode(log); ok {
			return &Event{Name: def.name, Signature: def.signature, Args: args}
		}
	}
	return nil
}

// decode decodes the arguments of log, which has the topic of the definition.
// It reports whether log has the layout of the definition.
func (def definition) decode(log ethrpc.Log) ([]Arg, bool) {
	indexed := 0
	for _, p := range def.params {
		if p.indexed {
			indexed++
		}
	}
	if len(log.Topics) != indexed+1 {
		return nil, false
	}

	data := solabi.NewDecoder(log.Data)
	topic, word := 1, 0
	args := make([]Arg, len(def.params))
	for i, p := range def.params {
		d, at := data, word
		if p.indexed {
			d, at = solabi.NewDecoder(log.Topics[topic][:]), 0
			topic++
		} else {
			word++
		}
		var value any
		switch p.typ {
		case "address":
			value = d.Address(at)
		case "uint256":
			value = d.Uint(at)
		case "uint8":
			value = d.Uint64(at)
			if value.(uint64) > 255 {
				return nil, false
			}
		case "bool":
			value = d.Bool(at)
		case "bytes32":
			value = ethrpc.Hash(d.Bytes32(at))
		case "bytes":
			value = ethrpc.Bytes(d.Bytes(at))
		}
		if d.Err() != nil {
			return nil, false
		}
		args[i] = Arg{Name: p.name, Type: p.typ, Value: value}
	}
	// The data has one word for each of its static parameters, and the bytes of its
	// dynamic ones after them. Without dynamic ones, there must be nothing else.
	if !def.dynamic() && len(log.Data) != 32*word {
		return nil, false
	}
	return args, true
}

// dynamic reports whether the definition has a parameter of dynamic size.
func (def definition) dynamic() bool {
	for _, p := range def.params {
		if p.typ == "bytes" {
			return true
		}
	}
	return false
}
