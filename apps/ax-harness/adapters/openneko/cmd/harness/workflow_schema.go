package main

// The host validates this shape again with the product's Zod schema. Keeping
// the Go admission schema explicit lets Ax reject malformed tool calls before
// they reach the broker.
const workflowSaveSchema = `{
  "type":"object",
  "required":["name","steps","expectedVersion"],
  "properties":{
    "name":{"type":"string","minLength":1,"maxLength":120},
    "description":{"type":"string","maxLength":2000},
    "goal":{"type":"string","maxLength":2000},
    "systemPromptOverlay":{"type":"string","maxLength":16000},
    "steps":{"type":"array","minItems":1,"maxItems":40,"items":{
      "type":"object","required":["id","description"],"properties":{
        "id":{"type":"string","minLength":1,"maxLength":60},
        "description":{"type":"string","minLength":1,"maxLength":2000}
      },"additionalProperties":false
    }},
    "batch":{"type":["object","null"],"properties":{
      "recordsField":{"type":"string"},
      "columns":{"type":"array","minItems":1,"maxItems":64,"items":{
        "type":"object","required":["name","path"],"properties":{
          "name":{"type":"string","minLength":1,"maxLength":80},
          "path":{"type":"string","minLength":1,"maxLength":256},
          "default":{"type":["string","number","boolean","null"]}
        },"additionalProperties":false
      }}
    },"required":["columns"],"additionalProperties":false},
    "triggers":{"type":"object","properties":{
      "cron":{"type":"string","minLength":1,"maxLength":120},
      "timezone":{"type":"string","minLength":1,"maxLength":80},
      "enabled":{"type":"boolean"},
      "when":{"type":"object","required":["table","primary_key"],"properties":{
        "table":{"type":"string","minLength":1,"maxLength":120},
        "where":{"type":"object"},
        "select":{"type":"array","items":{"type":"string","minLength":1,"maxLength":120}},
        "primary_key":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"string","minLength":1,"maxLength":120}},
        "version_column":{"type":"string","minLength":1,"maxLength":120},
        "enabled":{"type":"boolean"},
        "idempotency_key_template":{"type":"string","maxLength":200},
        "acknowledge_mutation_loop":{"type":"boolean"}
      },"additionalProperties":false},
      "watch":{"type":"object","required":["query","value_path","op"],"properties":{
        "query":{"type":"string","minLength":3,"maxLength":8000},
        "value_path":{"type":"string","minLength":1,"maxLength":400},
        "op":{"type":"string","enum":["gt","gte","lt","lte","eq","ne","changed"]},
        "threshold":{"type":["number","string"]},
        "cadence_seconds":{"type":"integer","minimum":60,"maximum":86400},
        "debounce_seconds":{"type":"integer","minimum":0,"maximum":604800},
        "severity":{"type":"string","enum":["low","medium","high","critical"]}
      },"additionalProperties":false}
    },"additionalProperties":false},
    "expectedVersion":{"type":"string","pattern":"^(absent|[0-9]{1,12})$"}
  },"additionalProperties":false
}`
