package resetwatch

import "hash/fnv"

type Joke struct {
	ID   string
	Tone string
	Text string
}

var DefaultJokes = []Joke{
	{ID: "tibo-ceiling", Tone: "spicy", Text: "Tibo moved the ceiling again."},
	{ID: "tibo-button", Tone: "spicy", Text: "Tibo touched the big red reset button."},
	{ID: "abundance", Tone: "spicy", Text: "OpenAI has briefly remembered abundance."},
	{ID: "ration-respawn", Tone: "spicy", Text: "The weekly ration has respawned."},
	{ID: "kiln", Tone: "spicy", Text: "Tibo restocked the kiln."},
	{ID: "quota-hydration", Tone: "normal", Text: "The quota is hydrated again."},
	{ID: "capital-alloc", Tone: "unhinged", Text: "Capital has been reallocated to the inference mines."},
	{ID: "scheduler-merciful", Tone: "normal", Text: "The scheduler has chosen mercy."},
	{ID: "backend-blessed", Tone: "spicy", Text: "The backend blinked and the limits came back clean."},
	{ID: "weekly-loot", Tone: "spicy", Text: "Weekly loot table rerolled."},
}

type CatalogJokeChooser struct {
	Tone  string
	Jokes []Joke
}

func (c CatalogJokeChooser) Choose(event Event) string {
	jokes := c.Jokes
	if len(jokes) == 0 {
		jokes = DefaultJokes
	}
	var candidates []Joke
	for _, joke := range jokes {
		if c.Tone == "" || c.Tone == joke.Tone {
			candidates = append(candidates, joke)
		}
	}
	if len(candidates) == 0 {
		candidates = jokes
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(event.ID))
	choice := h.Sum64() % uint64(len(candidates))
	var idx uint64
	for _, joke := range candidates {
		if idx == choice {
			return joke.ID
		}
		idx++
	}
	return candidates[0].ID
}

func JokeText(id string) string {
	for _, joke := range DefaultJokes {
		if joke.ID == id {
			return joke.Text
		}
	}
	return ""
}
