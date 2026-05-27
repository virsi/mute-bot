package domain

// Topic describes a preset thematic bucket the classifier maps clusters into.
type Topic struct {
	ID          string
	Title       string
	Description string
}

// PresetTopics is the Phase-1 fixed list of topics every user picks from.
var PresetTopics = []Topic{
	{ID: "politics", Title: "Политика", Description: "Внутренняя и внешняя политика, выборы, законы, заявления политиков."},
	{ID: "it", Title: "IT/Tech", Description: "Технологии, ПО, стартапы, релизы, AI."},
	{ID: "crypto", Title: "Крипта", Description: "Криптовалюты, биржи, регуляция, DeFi, NFT."},
	{ID: "economy", Title: "Экономика", Description: "Макро, рынки, валюты, санкции, бизнес-новости."},
	{ID: "war", Title: "Война", Description: "Боевые действия, операции, потери, оружие."},
	{ID: "science", Title: "Наука", Description: "Открытия, исследования, медицина, космос."},
	{ID: "sport", Title: "Спорт", Description: "Матчи, турниры, переходы, рекорды."},
}
