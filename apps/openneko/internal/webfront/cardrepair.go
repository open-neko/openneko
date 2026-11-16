package webfront

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

var cardRepairer = llm.Program{
	Signature: `cards:string "card JSON that failed the check", problems:string "what the check found wrong, one per line", schema:string "the JSON Schema the cards must match" -> fixedCards:string "the corrected card JSON object"`,
	Instruction: `You correct card JSON for OpenNeko. Change only what the problems name, so the cards match the schema. ` +
		`Keep every title, label, figure, row and cell exactly as given. Remove an optional field that you cannot correct. ` +
		`Return only the JSON object, with no fence and no other text.`,
}

func (a *API) cardRepair(orgID string) cardRepair {
	return func(ctx context.Context, cards string, problems []string, rich bool) (string, error) {
		shape := coreCardShape
		if rich {
			shape = richCardShape
		}
		log.Printf("[ax] card repair: %s", strings.Join(problems, "; "))
		res, err := a.hostCalls().Generate(ctx, orgID, "turn", cardRepairer, map[string]any{
			"cards": strings.TrimSpace(cards), "problems": strings.Join(problems, "\n"), "schema": jsStringify(shape.jsonSchema(), false)})
		if err != nil {
			return "", err
		}
		fixed := strings.TrimSpace(jsValueString(res.Outputs["fixedCards"]))
		fixed = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(fixed, "```json"), "```"), "```"))
		if fixed == "" {
			return "", errors.New("no cards in the reply")
		}
		return fixed, nil
	}
}
