package Communication

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/hyperledger/fabric-chaincode-go/shim"
	"github.com/hyperledger/fabric-contract-api-go/contractapi"
)

type ConfidentialMessageBySender struct {
	Sender    string   `json:"sender"`
	Receivers []string `json:"receivers"`
	Message   []byte   `json:"message"`
	Note      string   `json:"note"`
	//Number   int    `json:"number"`
}

type MessageForReceiver struct {
	Sender   string   `json:"sender"`
	Receiver string   `json:"receiver"`
	Messages [][]byte `json:"messages"`
	Notes    []string `json:"notes"`
	//Number   int    `json:"number"`
}

type ReturnMessages struct {
	Messages []string `json:"messages"`
	Notes    []string `json:"notes"`
	//Number   int    `json:"number"`
}

type SmartContract struct {
	contractapi.Contract
}

// sender给receiver发送消息时，为了使receiver能够知道有消息到来，需要在公共区域创建一个消息到来的通知
func (s *SmartContract) CreateMessageNotice(ctx contractapi.TransactionContextInterface, sender string, receiver string) error {

	key, err := ctx.GetStub().CreateCompositeKey("mn", []string{receiver, sender})
	if err != nil {
		return fmt.Errorf("failed to create composite key: %s", err.Error())
	}

	messageNoticeBytes, err := ctx.GetStub().GetState(key)
	if err != nil {
		return fmt.Errorf("failed to read message notice: %s", err.Error())
	}
	if bytes.Equal(messageNoticeBytes, []byte("1")) {
		// notice存在，但是未被读，此时什么都不做
		return nil
	}

	// notice不存在，说明是第一次发送消息
	// 或者notice存在，但是已被读，此时更新
	err = ctx.GetStub().PutState(key, []byte("1"))
	if err != nil {
		return fmt.Errorf("failed to put value: %s", err.Error())
	}
	return nil
}

// receiver读消息时，查找消息通知确定谁给自己发消息了
func (s *SmartContract) ReadMessageNotice(ctx contractapi.TransactionContextInterface, receiver string) ([]string, error) {

	rs, err := ctx.GetStub().GetStateByPartialCompositeKey("mn", []string{receiver})
	if err != nil {
		return nil, fmt.Errorf("failed to create composite key: %s", err.Error())
	}
	defer rs.Close()

	var senders []string

	for rs.HasNext() {
		item, err := rs.Next()
		if err != nil {
			return nil, err
		}
		// fmt.Println("item.Key, item.Value: ")
		// fmt.Println(item.Key, item.Value)

		if bytes.Equal(item.Value, []byte("1")) {
			// notice存在，未被读，说明来了新消息
			_, keyPart, err := ctx.GetStub().SplitCompositeKey(item.Key)
			if err != nil {
				return nil, fmt.Errorf("failed to split composite key: %s", err.Error())
			}
			sender := keyPart[1]
			senders = append(senders, sender)

			key, err := ctx.GetStub().CreateCompositeKey("mn", []string{receiver, sender})
			if err != nil {
				return nil, fmt.Errorf("failed to create composite key: %s", err.Error())
			}

			err = ctx.GetStub().PutState(key, []byte("0"))
			if err != nil {
				return nil, fmt.Errorf("failed to put value: %s", err.Error())
			}
		} else {
			// notice存在，已被读，还是原来的旧消息
			// 这时候如果后端数据库没有保存已读的消息，那么还是需要从链上读
			// 否则不需要，正常来讲应该不需要了。但是这里假设后端为了简便没保存已读消息
			_, keyPart, err := ctx.GetStub().SplitCompositeKey(item.Key)
			if err != nil {
				return nil, fmt.Errorf("failed to split composite key: %s", err.Error())
			}
			sender := keyPart[0]
			senders = append(senders, sender)
		}
	}
	return senders, nil
}

func (s *SmartContract) CreateConfidentialMessageBySender(ctx contractapi.TransactionContextInterface) error {

	transMap, err := ctx.GetStub().GetTransient()
	if err != nil {
		return fmt.Errorf("Error getting transient: " + err.Error())
	}

	// Marble properties are private, therefore they get passed in transient field
	transientMessageJSON, ok := transMap["message"]
	if !ok {
		return fmt.Errorf("message not found in the transient map")
	}

	var messageInput ConfidentialMessageBySender
	err = json.Unmarshal(transientMessageJSON, &messageInput)
	if err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %s", err.Error())
	}

	if len(messageInput.Sender) == 0 {
		return fmt.Errorf("sender field must be a non-empty string")
	}
	if len(messageInput.Receivers) == 0 {
		return fmt.Errorf("receivers field must be a non-empty string")
	}
	if len(messageInput.Message) == 0 {
		return fmt.Errorf("message field must be a non-empty string")
	}
	if len(messageInput.Note) == 0 {
		return fmt.Errorf("note field must be a non-empty string")
	}

	// Get the MSP ID of submitting client identity
	clientMSPID, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return fmt.Errorf("failed to get verified MSPID: %v", err)
	}
	if messageInput.Sender+"MSP" != clientMSPID {
		return fmt.Errorf("sender %s and client MSPID %s is not match: %v", messageInput.Sender, clientMSPID, err)
	}

	err = verifyClientOrgMatchesPeerOrg(ctx)
	if err != nil {
		return fmt.Errorf("CreateMessage cannot be performed: Error %v", err)
	}

	for i := 0; i < len(messageInput.Receivers); i++ {
		// ==== Check if message already exists ====
		var messages [][]byte
		var notes []string

		oldMessageAsBytes, err := ctx.GetStub().GetPrivateData(messageInput.Sender+"MSPCollection", messageInput.Receivers[i])
		if err != nil {
			return fmt.Errorf("Failed to get message: " + err.Error())
		} else if oldMessageAsBytes != nil {
			var oldMessage MessageForReceiver
			err = json.Unmarshal(oldMessageAsBytes, &oldMessage)
			if err != nil {
				return fmt.Errorf("failed to unmarshal JSON: %v", err)
			}
			messages = oldMessage.Messages
			messages = append(messages, messageInput.Message)
			notes = oldMessage.Notes
			notes = append(notes, messageInput.Note)
		} else if oldMessageAsBytes == nil {
			messages = append(messages, messageInput.Message)
			notes = append(notes, messageInput.Note)
		}

		// ==== Create message object, marshal to JSON, and update to state ====
		newMessage := MessageForReceiver{
			Sender:   messageInput.Sender,
			Receiver: messageInput.Receivers[i],
			Messages: messages,
			Notes:    notes,
		}

		newMessageJSONasBytes, err := json.Marshal(newMessage)
		if err != nil {
			return fmt.Errorf(err.Error())
		}

		err = ctx.GetStub().PutPrivateData(messageInput.Sender+"MSPCollection", messageInput.Receivers[i], newMessageJSONasBytes)
		if err != nil {
			return fmt.Errorf("failed to put Marble: %s", err.Error())
		}

		err = s.CreateMessageNotice(ctx, newMessage.Sender, newMessage.Receiver)
		if err != nil {
			return fmt.Errorf("failed to create message notice: %s", err.Error())
		}
	}
	return nil
}

// 一次性输出某个接收者的所有消息，但是这个函数的功能无法实现，可以忽略
// func (s *SmartContract) ReadAllConfidentialMessageByReceiver(ctx contractapi.TransactionContextInterface, receiver string) ([]MessageForReceiver, error) {
// 	// Get the MSP ID of submitting client identity
// 	clientMSPID, err := ctx.GetClientIdentity().GetMSPID()
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to get verified MSPID: %v", err)
// 	}
// 	if receiver+"MSP" != clientMSPID {
// 		return nil, fmt.Errorf("receiver is client MSPID is not match: %v", err)
// 	}

// 	// err = verifyClientOrgMatchesPeerOrg(ctx)
// 	// if err != nil {
// 	// 	return nil, fmt.Errorf("CreateMessage cannot be performed: Error %v", err)
// 	// }

// 	senders, err := s.ReadMessageNotice(ctx, receiver)
// 	if err != nil {
// 		return nil, fmt.Errorf("%s read message notice failed: %v", receiver, err)
// 	}

// 	var messageToReceivers []MessageForReceiver
// 	for i := 0; i < len(senders); i++ {
// 		messages, err := s.ReadConfidentialMessage(ctx, senders[i], receiver)
// 		if err != nil {
// 			return nil, fmt.Errorf("%s read %s message failed: %v", receiver, senders[i], err)
// 		}
// 		newMessage := MessageForReceiver{
// 			Sender:   senders[i],
// 			Receiver: receiver,
// 			Messages: messages.Messages,
// 			Notes:    messages.Notes,
// 		}

// 		messageToReceivers = append(messageToReceivers, newMessage)
// 	}
// 	return messageToReceivers, nil
// }

func (s *SmartContract) ReadConfidentialMessage(ctx contractapi.TransactionContextInterface, sender string, receiver string) (*ReturnMessages, error) {
	// Get the MSP ID of submitting client identity
	clientMSPID, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return nil, fmt.Errorf("failed to get verified MSPID: %v", err)
	}
	if receiver+"MSP" != clientMSPID {
		return nil, fmt.Errorf("receiver and client MSPID is not match")
	}

	// err = verifyClientOrgMatchesPeerOrg(ctx)
	// if err != nil {
	// 	return nil, fmt.Errorf("CreateMessage cannot be performed: Error %v", err)
	// }

	messageAsBytes, err := ctx.GetStub().GetPrivateData(sender+"MSPCollection", receiver)
	if err != nil {
		return nil, fmt.Errorf("Failed to get message: " + err.Error())
	} else if messageAsBytes == nil {
		return nil, fmt.Errorf("there is no messgae to %s in %s", receiver, sender+"MSPCollection")
	}

	var message MessageForReceiver
	err = json.Unmarshal(messageAsBytes, &message)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	var messagesStr []string
	for i := 0; i < len(message.Messages); i++ {
		messagesStr = append(messagesStr, string(message.Messages[i]))
	}

	resMessages := ReturnMessages{
		Messages: messagesStr,
		Notes:    message.Notes,
	}

	return &resMessages, nil
}

// MessageNoticeExists returns true when messageNotice with given ID exists in world state
func (s *SmartContract) MessageNoticeExists(ctx contractapi.TransactionContextInterface, key string) (bool, error) {
	messageNoticeJSON, err := ctx.GetStub().GetState(key)
	if err != nil {
		return false, fmt.Errorf("failed to read from world state: %v", err)
	}

	return messageNoticeJSON != nil, nil
}

// verifyClientOrgMatchesPeerOrg is an internal function used verify client org id and matches peer org id.
func verifyClientOrgMatchesPeerOrg(ctx contractapi.TransactionContextInterface) error {
	clientMSPID, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return fmt.Errorf("failed getting the client's MSPID: %v", err)
	}
	peerMSPID, err := shim.GetMSPID()
	if err != nil {
		return fmt.Errorf("failed getting the peer's MSPID: %v", err)
	}

	if clientMSPID != peerMSPID {
		return fmt.Errorf("client from org %v is not authorized to read or write private data from an org %v peer", clientMSPID, peerMSPID)
	}

	return nil
}
