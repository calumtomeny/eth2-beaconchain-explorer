package handlers

import (
	"encoding/hex"
	"encoding/json"
	"eth2-exporter/db"
	"eth2-exporter/services"
	"eth2-exporter/types"
	"eth2-exporter/utils"
	"eth2-exporter/version"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/juliangruber/go-intersect"

	"github.com/gorilla/mux"
)

var blockTemplate = template.Must(template.New("block").Funcs(utils.GetTemplateFuncs()).ParseFiles("templates/layout.html", "templates/block.html"))
var blockNotFoundTemplate = template.Must(template.New("blocknotfound").ParseFiles("templates/layout.html", "templates/blocknotfound.html"))

// Block will return the data for a block
func Block(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")

	vars := mux.Vars(r)

	slotOrHash := strings.Replace(vars["slotOrHash"], "0x", "", -1)
	blockSlot := int64(-1)
	blockRootHash, err := hex.DecodeString(slotOrHash)
	if err != nil || len(slotOrHash) != 64 {
		blockRootHash = []byte{}
		blockSlot, err = strconv.ParseInt(vars["slotOrHash"], 10, 64)
	}

	data := &types.PageData{
		HeaderAd: true,
		Meta: &types.Meta{
			Description: "beaconcha.in makes the Ethereum 2.0. beacon chain accessible to non-technical end users",
			GATag:       utils.Config.Frontend.GATag,
		},
		ShowSyncingMessage:    services.IsSyncing(),
		Active:                "blocks",
		Data:                  nil,
		User:                  getUser(w, r),
		Version:               version.Version,
		ChainSlotsPerEpoch:    utils.Config.Chain.SlotsPerEpoch,
		ChainSecondsPerSlot:   utils.Config.Chain.SecondsPerSlot,
		ChainGenesisTimestamp: utils.Config.Chain.GenesisTimestamp,
		CurrentEpoch:          services.LatestEpoch(),
		CurrentSlot:           services.LatestSlot(),
		FinalizationDelay:     services.FinalizationDelay(),
	}

	if err != nil {
		data.Meta.Title = fmt.Sprintf("%v - Slot %v - beaconcha.in - %v", utils.Config.Frontend.SiteName, slotOrHash, time.Now().Year())
		data.Meta.Path = "/block/" + slotOrHash
		//logger.Errorf("error retrieving block data: %v", err)
		err = blockNotFoundTemplate.ExecuteTemplate(w, "layout", data)

		if err != nil {
			logger.Errorf("error executing template for %v route: %v", r.URL.String(), err)
			http.Error(w, "Internal server error", 503)
			return
		}
		return
	}

	blockPageData := types.BlockPageData{}
	err = db.DB.Get(&blockPageData, `
		SELECT
			epoch,
			slot,
			blockroot,
			parentroot,
			stateroot,
			signature,
			randaoreveal,
			graffiti,
			eth1data_depositroot,
			eth1data_depositcount,
			eth1data_blockhash,
			proposerslashingscount,
			attesterslashingscount,
			attestationscount,
			depositscount,
			voluntaryexitscount,
			proposer,
			status,
			COALESCE(validators.name, '') AS name
		FROM blocks 
		LEFT JOIN validators ON blocks.proposer = validators.validatorindex
		WHERE slot = $1 OR blockroot = $2 ORDER BY status LIMIT 1`,
		blockSlot, blockRootHash)

	if err != nil {
		data.Meta.Title = fmt.Sprintf("%v - Slot %v - beaconcha.in - %v", utils.Config.Frontend.SiteName, slotOrHash, time.Now().Year())
		data.Meta.Path = "/block/" + slotOrHash
		//logger.Errorf("error retrieving block data: %v", err)
		err = blockNotFoundTemplate.ExecuteTemplate(w, "layout", data)

		if err != nil {
			logger.Errorf("error executing template for %v route: %v", r.URL.String(), err)
			http.Error(w, "Internal server error", 503)
			return
		}
		return
	}

	data.Meta.Title = fmt.Sprintf("%v - Slot %v - beaconcha.in - %v", utils.Config.Frontend.SiteName, blockPageData.Slot, time.Now().Year())
	data.Meta.Path = fmt.Sprintf("/block/%v", blockPageData.Slot)

	blockPageData.Ts = utils.SlotToTime(blockPageData.Slot)
	blockPageData.SlashingsCount = blockPageData.AttesterSlashingsCount + blockPageData.ProposerSlashingsCount

	err = db.DB.Get(&blockPageData.NextSlot, "SELECT slot FROM blocks WHERE slot > $1 ORDER BY slot LIMIT 1", blockPageData.Slot)
	if err != nil {
		logger.Errorf("error retrieving next slot for block %v: %v", blockPageData.Slot, err)
		blockPageData.NextSlot = 0
	}
	err = db.DB.Get(&blockPageData.PreviousSlot, "SELECT slot FROM blocks WHERE slot < $1 ORDER BY slot DESC LIMIT 1", blockPageData.Slot)
	if err != nil {
		logger.Errorf("error retrieving previous slot for block %v: %v", blockPageData.Slot, err)
		blockPageData.PreviousSlot = 0
	}

	var attestations []*types.BlockPageAttestation
	rows, err := db.DB.Query(`
		SELECT
			block_slot,
			block_index,
			aggregationbits,
			validators,
			signature,
			slot,
			committeeindex,
			beaconblockroot,
			source_epoch,
			source_root,
			target_epoch,
			target_root
		FROM blocks_attestations
		WHERE block_slot = $1
		ORDER BY block_index`,
		blockPageData.Slot)
	if err != nil {
		logger.Errorf("error retrieving block attestation data: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}
	defer rows.Close()

	for rows.Next() {
		attestation := &types.BlockPageAttestation{}

		err := rows.Scan(
			&attestation.BlockSlot,
			&attestation.BlockIndex,
			&attestation.AggregationBits,
			&attestation.Validators,
			&attestation.Signature,
			&attestation.Slot,
			&attestation.CommitteeIndex,
			&attestation.BeaconBlockRoot,
			&attestation.SourceEpoch,
			&attestation.SourceRoot,
			&attestation.TargetEpoch,
			&attestation.TargetRoot)
		if err != nil {
			logger.Errorf("error scanning block attestation data: %v", err)
			http.Error(w, "Internal server error", 503)
			return
		}
		attestations = append(attestations, attestation)
	}
	blockPageData.Attestations = attestations

	var votes []*types.BlockVote
	rows, err = db.DB.Query(`
		SELECT
			block_slot,
			validators,
			committeeindex
		FROM blocks_attestations
		WHERE beaconblockroot = $1
		ORDER BY committeeindex`,
		blockPageData.BlockRoot)
	if err != nil {
		logger.Errorf("error retrieving block votes data: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}
	defer rows.Close()

	for rows.Next() {
		attestation := &types.BlockPageAttestation{}

		err := rows.Scan(
			&attestation.BlockSlot,
			&attestation.Validators,
			&attestation.CommitteeIndex)
		if err != nil {
			logger.Errorf("error scanning block votes data: %v", err)
			http.Error(w, "Internal server error", 503)
			return
		}
		for _, validator := range attestation.Validators {
			votes = append(votes, &types.BlockVote{
				Validator:      uint64(validator),
				IncludedIn:     attestation.BlockSlot,
				CommitteeIndex: attestation.CommitteeIndex,
			})
		}
	}
	blockPageData.Votes = votes
	sort.Slice(blockPageData.Votes, func(i, j int) bool {
		return blockPageData.Votes[i].Validator < blockPageData.Votes[j].Validator
	})
	blockPageData.VotesCount = uint64(len(blockPageData.Votes))

	var deposits []*types.BlockPageDeposit
	err = db.DB.Select(&deposits, `
		SELECT
			publickey,
			withdrawalcredentials,
			amount,
			signature
		FROM blocks_deposits
		WHERE block_slot = $1
		ORDER BY block_index`,
		blockPageData.Slot)
	if err != nil {
		logger.Errorf("error retrieving block deposit data: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	blockPageData.Deposits = deposits

	err = db.DB.Select(&blockPageData.VoluntaryExits, "SELECT validatorindex, signature FROM blocks_voluntaryexits WHERE block_slot = $1", blockPageData.Slot)
	if err != nil {
		logger.Errorf("error retrieving block deposit data: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	err = db.DB.Select(&blockPageData.AttesterSlashings, `
		SELECT
			block_slot,
			block_index,
			attestation1_indices,
			attestation1_signature,
			attestation1_slot,
			attestation1_index,
			attestation1_beaconblockroot,
			attestation1_source_epoch,
			attestation1_source_root,
			attestation1_target_epoch,
			attestation1_target_root,
		    attestation2_indices,
			attestation2_signature,
			attestation2_slot,
			attestation2_index,
			attestation2_beaconblockroot,
			attestation2_source_epoch,
			attestation2_source_root,
			attestation2_target_epoch,
			attestation2_target_root
			FROM blocks_attesterslashings
		WHERE block_slot = $1`, blockPageData.Slot)
	if err != nil {
		logger.Errorf("error retrieving block attester slashings data: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	if len(blockPageData.AttesterSlashings) > 0 {
		for _, slashing := range blockPageData.AttesterSlashings {
			inter := intersect.Simple(slashing.Attestation1Indices, slashing.Attestation2Indices)

			for _, i := range inter {
				slashing.SlashedValidators = append(slashing.SlashedValidators, i.(int64))
			}
		}
	}

	err = db.DB.Select(&blockPageData.ProposerSlashings, "SELECT * FROM blocks_proposerslashings WHERE block_slot = $1", blockPageData.Slot)
	if err != nil {
		logger.Errorf("error retrieving block proposer slashings data: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	data.Data = blockPageData

	if utils.IsApiRequest(r) {
		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(data.Data)
	} else {
		w.Header().Set("Content-Type", "text/html")
		err = blockTemplate.ExecuteTemplate(w, "layout", data)
	}

	if err != nil {
		logger.Errorf("error executing template for %v route: %v", r.URL.String(), err)
		http.Error(w, "Internal server error", 503)
		return
	}
}
