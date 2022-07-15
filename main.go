package main

import (
	"fmt"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"
	"Chaincode/Communication"
)

func main() {

	chaincode, err := contractapi.NewChaincode(&Communication.SmartContract{})

	if err != nil {
		fmt.Printf("Error creating private communication chaincode: %s", err.Error())
		return
	}

	if err := chaincode.Start(); err != nil {
		fmt.Printf("Error starting private communication chaincode: %s", err.Error())
	}
}
