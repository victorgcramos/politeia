// Copyright (c) 2017-2019 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package commands

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/decred/politeia/politeiad/api/v1/mime"
	"github.com/decred/politeia/politeiawww/api/cms/v1"
	www "github.com/decred/politeia/politeiawww/api/www/v1"
	"github.com/decred/politeia/util"
)

// NewInvoiceCmd submits a new invoice.
type NewInvoiceCmd struct {
	Args struct {
		Month       string   `positional-arg-name:"month"`           // Invoice Month
		Year        string   `positional-arg-name:"year"`            // Invoice Year
		CSV         string   `positional-arg-name:"csvfile"`         // Invoice CSV file
		Attachments []string `positional-arg-name:"attachmentfiles"` // Invoice attachment files
	} `positional-args:"true" optional:"true"`
}

// Execute executes the new invoice command.
func (cmd *NewInvoiceCmd) Execute(args []string) error {
	csvFile := cmd.Args.CSV
	attachmentFiles := cmd.Args.Attachments

	month, err := strconv.Atoi(cmd.Args.Month)
	if err != nil {
		return err
	}

	year, err := strconv.Atoi(cmd.Args.Year)
	if err != nil {
		return err
	}

	if csvFile == "" {
		return errInvoiceCSVNotFound
	}

	// Check for user identity
	if cfg.Identity == nil {
		return errUserIdentityNotFound
	}

	// Get server public key
	vr, err := client.Version()
	if err != nil {
		return err
	}

	var csv []byte
	files := make([]www.File, 0, www.PolicyMaxImages+1)
	// Read csv file into memory and convert to type File
	fpath := util.CleanAndExpandPath(csvFile)

	csv, err = ioutil.ReadFile(fpath)
	if err != nil {
		return fmt.Errorf("ReadFile %v: %v", fpath, err)
	}

	invInput, err := validateParseCSV(csv)
	if err != nil {
		return fmt.Errorf("Parsing CSV failed: %v", err)
	}

	invInput.Month = uint16(month)
	invInput.Year = uint16(year)

	b, err := json.Marshal(invInput)
	if err != nil {
		return fmt.Errorf("Marshal: %v", err)
	}

	f := www.File{
		Name:    "invoice.json",
		MIME:    mime.DetectMimeType(b),
		Digest:  hex.EncodeToString(util.Digest(b)),
		Payload: base64.StdEncoding.EncodeToString(b),
	}

	files = append(files, f)

	// Read attachment files into memory and convert to type File
	for _, file := range attachmentFiles {
		path := util.CleanAndExpandPath(file)
		attachment, err := ioutil.ReadFile(path)
		if err != nil {
			return fmt.Errorf("ReadFile %v: %v", path, err)
		}

		f := www.File{
			Name:    filepath.Base(file),
			MIME:    mime.DetectMimeType(attachment),
			Digest:  hex.EncodeToString(util.Digest(attachment)),
			Payload: base64.StdEncoding.EncodeToString(attachment),
		}

		files = append(files, f)
	}

	// Compute merkle root and sign it
	sig, err := signedMerkleRoot(files, cfg.Identity)
	if err != nil {
		return fmt.Errorf("SignMerkleRoot: %v", err)
	}

	// Setup new proposal request
	ni := &v1.NewInvoice{
		Files:     files,
		PublicKey: hex.EncodeToString(cfg.Identity.Public.Key[:]),
		Signature: sig,
		Month:     uint16(month),
		Year:      uint16(year),
	}

	// Print request details
	err = printJSON(ni)
	if err != nil {
		return err
	}

	// Send request
	nir, err := client.NewInvoice(ni)
	if err != nil {
		return err
	}

	// Verify the censorship record
	pr := www.ProposalRecord{
		Files:            ni.Files,
		PublicKey:        ni.PublicKey,
		Signature:        ni.Signature,
		CensorshipRecord: nir.CensorshipRecord,
	}
	err = verifyProposal(pr, vr.PubKey)
	if err != nil {
		return fmt.Errorf("unable to verify proposal %v: %v",
			pr.CensorshipRecord.Token, err)
	}

	// Print response details
	return printJSON(nir)
}

func validateParseCSV(data []byte) (*v1.InvoiceInput, error) {
	LineItemType := map[string]v1.LineItemTypeT{
		"labor":   v1.LineItemTypeLabor,
		"expense": v1.LineItemTypeExpense,
		"misc":    v1.LineItemTypeMisc,
	}
	invInput := &v1.InvoiceInput{}

	// Validate that the invoice is CSV-formatted.
	csvReader := csv.NewReader(strings.NewReader(string(data)))
	csvReader.Comma = www.PolicyInvoiceFieldDelimiterChar
	csvReader.Comment = www.PolicyInvoiceCommentChar
	csvReader.TrimLeadingSpace = true

	csvFields, err := csvReader.ReadAll()
	if err != nil {
		return invInput, err
	}

	lineItems := make([]v1.LineItemsInput, 0, len(csvFields))
	// Validate that line items are the correct length and contents in
	// field 4 and 5 are parsable to integers
	for i, lineContents := range csvFields {
		lineItem := v1.LineItemsInput{}
		if len(lineContents) != www.PolicyInvoiceLineItemCount {
			return invInput, www.UserError{
				ErrorCode: www.ErrorStatusMalformedInvoiceFile,
			}
		}
		hours, err := strconv.ParseFloat(lineContents[4], 64)
		if err != nil {
			return invInput, www.UserError{
				ErrorCode: www.ErrorStatusMalformedInvoiceFile,
			}
		}
		cost, err := strconv.ParseFloat(lineContents[5], 64)
		if err != nil {
			return invInput, www.UserError{
				ErrorCode: www.ErrorStatusMalformedInvoiceFile,
			}
		}
		lineItem.LineNumber = uint16(i)

		lineItemType, ok := LineItemType[strings.ToLower(lineContents[0])]
		if !ok {
			return invInput, www.UserError{
				ErrorCode: www.ErrorStatusMalformedInvoiceFile,
			}
		}
		lineItem.Type = lineItemType
		lineItem.Subtype = lineContents[1]
		lineItem.Description = lineContents[2]
		lineItem.ProposalToken = lineContents[3]
		lineItem.Hours = hours
		lineItem.TotalCost = cost
		lineItems = append(lineItems, lineItem)
	}
	invInput.LineItems = lineItems

	return invInput, nil
}

const newInvoiceHelpMsg = `newinvoice [flags] "csvFile" "attachmentFiles" 

Submit a new invoice to Politeia. Invoice must be a csv file. Accepted 
attachment filetypes: png or plain text.

Arguments:
1. month			 (string, required)   Month (MM, 01-12)
2. year				 (string, required)   Year (YYYY)
3. csvFile			 (string, required)   Invoice CSV file
4. attachmentFiles	 (string, optional)   Attachments 

Result:
{
  "files": [
    {
      "name":      (string)  Filename 
      "mime":      (string)  Mime type 
      "digest":    (string)  File digest 
      "payload":   (string)  File payload 
    }
  ],
  "publickey":   (string)  Public key of user
  "signature":   (string)  Signed merkel root of files in invoice
}`
