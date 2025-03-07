package models

type OnvifAction struct {
	Action  string      `json:"action" bson:"action"`
	Payload interface{} `json:"payload" bson:"payload"`
}

type OnvifActionPTZ struct {
	Left   int     `json:"left" bson:"left"`
	Right  int     `json:"right" bson:"right"`
	Up     int     `json:"up" bson:"up"`
	Down   int     `json:"down" bson:"down"`
	Center int     `json:"center" bson:"center"`
	Zoom   float64 `json:"zoom" bson:"zoom"`
}
