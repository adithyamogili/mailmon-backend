package worker

import "encoding/json"

const TypeProcessOnDemand = "email:process_on_demand"

type OnDemandPayload struct {
	UserID string `json:"user_id"`
	ReplyTo string `json:"reply_to"`
}

func NewOnDemandTask(userID, replyTo string) ([]byte, error) {
	return json.Marshal(OnDemandPayload{UserID: userID, ReplyTo: replyTo})
}
