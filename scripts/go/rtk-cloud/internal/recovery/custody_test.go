package recovery

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"

	"filippo.io/age"
)

func custodyFixture(t *testing.T) (Custody, []*age.X25519Identity, []string) {
	t.Helper()
	c := Custody{Version: 1, Environment: "prod", ClusterID: "original-raft-cluster", Shares: 5, Threshold: 3}
	var identities []*age.X25519Identity
	var shares []string
	for i := 0; i < 10; i++ {
		identity, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, identity)
		holder := CustodyRecipient{ShareID: i%5 + 1, Custodian: fmt.Sprintf("custodian-%d", i), Location: fmt.Sprintf("independent-location-%d", i), AgeRecipient: identity.Recipient().String(), EnvelopeSHA256: strings.Repeat("a", 64), DeliveryReceiptSHA256: strings.Repeat("b", 64)}
		if i < 5 {
			c.Primary = append(c.Primary, holder)
			shares = append(shares, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 33)))
		} else {
			c.Escrow = append(c.Escrow, holder)
		}
	}
	return c, identities, shares
}

func TestCustodyDisallowsSingleLocationPersonKeyQuorumAndDependentEscrow(t *testing.T) {
	for _, bad := range []string{"location", "person", "key", "escrow-location", "escrow-person", "escrow-key", "missing-share", "missing-delivery"} {
		t.Run(bad, func(t *testing.T) {
			c, _, _ := custodyFixture(t)
			if err := c.Validate(true); err != nil {
				t.Fatal(err)
			}
			switch bad {
			case "location":
				for i := 0; i < 3; i++ {
					c.Primary[i].Location = "combined"
				}
			case "person":
				for i := 0; i < 3; i++ {
					c.Primary[i].Custodian = "same-person"
				}
			case "key":
				for i := 1; i < 3; i++ {
					c.Primary[i].AgeRecipient = c.Primary[0].AgeRecipient
				}
			case "escrow-location":
				c.Escrow[0].Location = c.Primary[0].Location
			case "escrow-person":
				c.Escrow[0].Custodian = c.Primary[0].Custodian
			case "escrow-key":
				c.Escrow[0].AgeRecipient = c.Primary[0].AgeRecipient
			case "missing-share":
				c.Primary[0].ShareID = c.Primary[1].ShareID
			case "missing-delivery":
				c.Primary[0].DeliveryReceiptSHA256 = ""
			}
			if c.Validate(true) == nil {
				t.Fatal("incomplete or concentrated custody accepted")
			}
		})
	}
}

func TestCustodyEnvelopesUseIndependentRecipientKeysWithoutPlaintextBundle(t *testing.T) {
	c, identities, shares := custodyFixture(t)
	envelopes, err := EncryptCustodyShares(c, shares)
	if err != nil || len(envelopes) != 10 {
		t.Fatal(err)
	}
	for i, envelope := range envelopes {
		r, err := age.Decrypt(bytes.NewReader(envelope), identities[i])
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(r)
		want, _ := base64.StdEncoding.DecodeString(shares[i%5])
		if err != nil || !bytes.Equal(raw, want) || bytes.Contains(envelope, want) {
			t.Fatal("custody encryption differs or exposes a share")
		}
		if _, err := age.Decrypt(bytes.NewReader(envelope), identities[(i+1)%10]); err == nil {
			t.Fatal("another custodian decrypted envelope")
		}
	}
	shares[1] = shares[0]
	if _, err := EncryptCustodyShares(c, shares); err == nil {
		t.Fatal("duplicate shares accepted")
	}
}
