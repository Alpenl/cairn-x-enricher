package classify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// PolicyHash uses recursively sorted JSON object keys, matching the Worker's
// canonical policy hash. Normalizing a struct through a map avoids field-order
// differences; policy values contain only fixed strings, booleans and numbers.
func PolicyHash(policy Policy) (string, error) {
	canonical, err := canonicalReplayJSON(policy)
	if err != nil {
		return "", err
	}
	return sha256Hex(canonical), nil
}

// WithPolicyHashIdentity validates the original identity's semantic guards
// before deriving a reproducible new identity. Distinct thresholds cannot
// collide, while repeated inspection of an equal candidate produces one key.
func WithPolicyHashIdentity(policy Policy) (Policy, string, error) {
	if err := policy.Validate(); err != nil {
		return Policy{}, "", err
	}
	policy.Version = ""
	semanticHash, err := PolicyHash(policy)
	if err != nil {
		return Policy{}, "", err
	}
	policy.Version = "replay-" + semanticHash[:24]
	if err := policy.Validate(); err != nil {
		return Policy{}, "", err
	}
	hash, err := PolicyHash(policy)
	return policy, hash, err
}

// PolicyReplayOperationKey binds an idempotent replay to the policy and active target.
func PolicyReplayOperationKey(linkID int64, runIDs []int64, policyHash string, contentRevision int64, specHash, requestedModel, resolvedModel string, targetGeneration int64) (string, error) {
	runIDs = append([]int64(nil), runIDs...)
	sort.Slice(runIDs, func(i, j int) bool { return runIDs[i] < runIDs[j] })
	encoded, err := canonicalReplayJSON(map[string]any{"run_ids": runIDs, "policy_hash": policyHash, "content_revision": contentRevision, "spec_hash": specHash, "requested_model": requestedModel, "resolved_model": resolvedModel, "target_generation": targetGeneration})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("replay-%d-%s", linkID, sha256Hex(encoded)), nil
}

// Worker canonicalJSON recursively sorts object keys and uses JSON.stringify
// scalar spelling. Go's default JSON differs at 1e-6 and on HTML characters.
func canonicalReplayJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	var write func(any) error
	write = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			output.WriteByte('{')
			for index, key := range keys {
				if index > 0 {
					output.WriteByte(',')
				}
				if err := write(key); err != nil {
					return err
				}
				output.WriteByte(':')
				if err := write(value[key]); err != nil {
					return err
				}
			}
			output.WriteByte('}')
		case []any:
			output.WriteByte('[')
			for index, item := range value {
				if index > 0 {
					output.WriteByte(',')
				}
				if err := write(item); err != nil {
					return err
				}
			}
			output.WriteByte(']')
		case json.Number:
			number, err := value.Float64()
			if err != nil {
				return err
			}
			absolute := number
			if absolute < 0 {
				absolute = -absolute
			}
			if absolute == 0 {
				output.WriteByte('0')
			} else if absolute >= 1e-6 && absolute < 1e21 {
				output.WriteString(strconv.FormatFloat(number, 'f', -1, 64))
			} else {
				parts := strings.Split(strconv.FormatFloat(number, 'e', -1, 64), "e")
				exponent, err := strconv.Atoi(parts[1])
				if err != nil {
					return err
				}
				output.WriteString(parts[0] + "e")
				if exponent >= 0 {
					output.WriteByte('+')
				}
				output.WriteString(strconv.Itoa(exponent))
			}
		case string:
			output.WriteByte('"')
			for _, char := range value {
				switch char {
				case '"', '\\':
					output.WriteByte('\\')
					output.WriteRune(char)
				case '\b':
					output.WriteString(`\b`)
				case '\f':
					output.WriteString(`\f`)
				case '\n':
					output.WriteString(`\n`)
				case '\r':
					output.WriteString(`\r`)
				case '\t':
					output.WriteString(`\t`)
				default:
					if char < 0x20 {
						_, _ = fmt.Fprintf(&output, `\u%04x`, char)
					} else {
						output.WriteRune(char)
					}
				}
			}
			output.WriteByte('"')
		default:
			scalar, err := json.Marshal(value)
			if err != nil {
				return err
			}
			output.Write(scalar)
		}
		return nil
	}
	if err := write(normalized); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
