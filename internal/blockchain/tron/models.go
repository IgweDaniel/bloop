package tron

import "encoding/json"

type blockResponse struct {
	BlockID     string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Number    uint64 `json:"number"`
			Timestamp int64  `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []transactionResponse `json:"transactions"`
}

type transactionResponse struct {
	TxID    string `json:"txID"`
	RawData struct {
		Contract []contractResponse `json:"contract"`
	} `json:"raw_data"`
	Ret []struct {
		ContractRet string `json:"contractRet"`
	} `json:"ret"`
}

type contractResponse struct {
	Type      string `json:"type"`
	Parameter struct {
		Value json.RawMessage `json:"value"`
	} `json:"parameter"`
}

type transferContract struct {
	OwnerAddress string `json:"owner_address"`
	ToAddress    string `json:"to_address"`
	Amount       int64  `json:"amount"`
}

type triggerSmartContract struct {
	ContractAddress string `json:"contract_address"`
}

type transactionInfoResponse struct {
	ID      string `json:"id"`
	TxID    string `json:"txID"`
	Fee     int64  `json:"fee"`
	Receipt struct {
		Result    string `json:"result"`
		EnergyFee int64  `json:"energy_fee"`
		NetFee    int64  `json:"net_fee"`
	} `json:"receipt"`
	Log []eventLog `json:"log"`
}

func (t transactionInfoResponse) txID() string {
	if t.ID != "" {
		return t.ID
	}
	return t.TxID
}

type eventLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}
