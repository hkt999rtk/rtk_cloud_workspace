package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"

	"filippo.io/age"
)

var custodySHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Custody records public routing and independently retained delivery receipts.
// It cannot contain an unseal share, passphrase, Root token or CA private key.
type Custody struct {
	Version     int                `json:"version"`
	Environment string             `json:"environment"`
	ClusterID   string             `json:"cluster_id"`
	Shares      int                `json:"shares"`
	Threshold   int                `json:"threshold"`
	Primary     []CustodyRecipient `json:"primary"`
	Escrow      []CustodyRecipient `json:"escrow"`
}
type CustodyRecipient struct {
	ShareID               int    `json:"share_id"`
	Custodian             string `json:"custodian"`
	Location              string `json:"location"`
	AgeRecipient          string `json:"age_recipient"`
	EnvelopeSHA256        string `json:"envelope_sha256,omitempty"`
	DeliveryReceiptSHA256 string `json:"delivery_receipt_sha256,omitempty"`
}

func (c Custody) Validate(requireDelivery bool) error {
	if c.Version != 1 || !Name.MatchString(c.Environment) || c.ClusterID == "" || c.Shares != 5 || c.Threshold != 3 {
		return errors.New("custody requires exact environment/cluster and five Shamir shares with threshold three")
	}
	locations := map[string]map[int]bool{}
	people := map[string]map[int]bool{}
	keys := map[string]map[int]bool{}
	primaryLocations, primaryPeople := map[string]bool{}, map[string]bool{}
	primaryKeys := map[string]bool{}
	for groupIndex, group := range [][]CustodyRecipient{c.Primary, c.Escrow} {
		if len(group) != 5 {
			return errors.New("custody requires all five primary and independent escrow recipients")
		}
		seen := map[int]bool{}
		for _, holder := range group {
			if holder.ShareID < 1 || holder.ShareID > 5 || seen[holder.ShareID] || strings.TrimSpace(holder.Custodian) == "" || holder.Custodian != strings.TrimSpace(holder.Custodian) || strings.TrimSpace(holder.Location) == "" || holder.Location != strings.TrimSpace(holder.Location) {
				return errors.New("custody share identity, named responsibility and location must be explicit")
			}
			seen[holder.ShareID] = true
			if _, err := age.ParseX25519Recipient(holder.AgeRecipient); err != nil {
				return errors.New("custody requires portable age recipients")
			}
			if groupIndex == 0 {
				primaryLocations[holder.Location], primaryPeople[holder.Custodian], primaryKeys[holder.AgeRecipient] = true, true, true
			} else if primaryLocations[holder.Location] || primaryPeople[holder.Custodian] || primaryKeys[holder.AgeRecipient] {
				return errors.New("escrow must be independent of primary locations, custodians and decryption keys")
			}
			if locations[holder.Location] == nil {
				locations[holder.Location] = map[int]bool{}
			}
			if people[holder.Custodian] == nil {
				people[holder.Custodian] = map[int]bool{}
			}
			if keys[holder.AgeRecipient] == nil {
				keys[holder.AgeRecipient] = map[int]bool{}
			}
			locations[holder.Location][holder.ShareID], people[holder.Custodian][holder.ShareID] = true, true
			keys[holder.AgeRecipient][holder.ShareID] = true
			if len(locations[holder.Location]) >= 3 || len(people[holder.Custodian]) >= 3 || len(keys[holder.AgeRecipient]) >= 3 {
				return errors.New("one location, custodian or decryption key can recover a Shamir quorum")
			}
			if requireDelivery && (!custodySHA256.MatchString(holder.EnvelopeSHA256) || !custodySHA256.MatchString(holder.DeliveryReceiptSHA256)) {
				return errors.New("custody requires encrypted-envelope digests and actual independent delivery receipts")
			}
		}
	}
	return nil
}

// EncryptCustodyShares receives init/rekey response shares in memory and returns
// only recipient-encrypted envelopes. No combined plaintext share file is made.
// Delivery remains incomplete until independently verified receipts are recorded.
func EncryptCustodyShares(c Custody, shares []string) ([][]byte, error) {
	if err := c.Validate(false); err != nil {
		return nil, err
	}
	if len(shares) != 5 {
		return nil, errors.New("exactly five original Shamir shares required")
	}
	decoded := make([][]byte, 5)
	defer func() {
		for _, raw := range decoded {
			clear(raw)
		}
	}()
	seen := map[string]bool{}
	for i, share := range shares {
		raw, err := base64.StdEncoding.DecodeString(share)
		if err != nil || len(raw) != 33 {
			return nil, errors.New("invalid Shamir share encoding")
		}
		sum := sha256.Sum256(raw)
		key := hex.EncodeToString(sum[:])
		if seen[key] {
			return nil, errors.New("duplicate Shamir share")
		}
		seen[key] = true
		decoded[i] = raw
	}
	var envelopes [][]byte
	for _, group := range [][]CustodyRecipient{c.Primary, c.Escrow} {
		for _, holder := range group {
			recipient, _ := age.ParseX25519Recipient(holder.AgeRecipient)
			var output bytes.Buffer
			w, err := age.Encrypt(&output, recipient)
			if err != nil {
				return nil, errors.New("cannot encrypt custody envelope")
			}
			if _, err = io.Copy(w, bytes.NewReader(decoded[holder.ShareID-1])); err != nil {
				return nil, errors.New("cannot encrypt custody envelope")
			}
			if err = w.Close(); err != nil {
				return nil, errors.New("cannot seal custody envelope")
			}
			envelopes = append(envelopes, output.Bytes())
		}
	}
	return envelopes, nil
}
