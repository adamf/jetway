# Writing standard

The prose in this repository (README, docs, the site) follows ASD-STE100
Simplified Technical English, adapted for a product that has no approved
dictionary. The rules below are the ones that apply here. Apply them to
every sentence you write or change.

## Sentences

- One topic per sentence. Descriptive sentences have at most 25 words.
  Instructions have at most 20 words.
- Use the active voice. Say who does what: "The core stores the state",
  not "the state is stored".
- Use the present tense for descriptions and the imperative for
  instructions: "Run `skyd`", not "you would run" or "you can run".
- Do not join independent clauses with a dash, a semicolon, or a colon.
  Start a new sentence.
- Do not use a dash or a pair of dashes to add an aside. Put the aside in
  its own sentence, or delete it.
- Use parentheses only for an abbreviation after its expansion, a unit, or
  a reference such as "(RFC 2351)".
- Do not use sentence fragments as sentences.

## Words

- Use one word for one thing, and the same word every time. The words for
  this product are: world, core, region, distribution system, switch,
  trunk, link, tenant, carrier, seat, department, decision, lever, tape,
  recording, replay, link secret, link token, seat token, the demo, the
  mirror world.
- Do not use: real (as an intensifier), honest, honestly, actually,
  actual, truly, simply, just, of course, nothing is mocked, for real,
  meant to be, worth, for the record, written down, the way a real X does,
  which is why, so that (to start a clause of consequence), not X but Y,
  not a guess, the truth is, it turns out, here is the thing, the pattern
  that keeps paying, a bar to beat, the bar.
- Do not use metaphors, idioms, mottos or slogans. Delete a sentence
  whose only job is rhythm or emphasis.
- Do not end a paragraph with a short aphorism ("The autopilot is the
  bar.", "Nothing moves except a message.").
- Do not build sentences in threes for effect, and do not repeat a
  sentence opening for effect.
- Do not personify the software. Say "the simulator sends a decision to
  the seat", not "the day asks you". Say "the core keeps the state", not
  "the state is the core's to keep".
- Do not use the possessive construction "X is Y's to do". Say "Y does X".
- Write numbers as digits with a space before the unit: 5,364 flights,
  4 GB, 160 s. Give a number only if the reader uses it.
- Give the full form of an abbreviation the first time it appears in a
  document, then the abbreviation.

## Structure

- One topic per paragraph, at most six sentences.
- Use a numbered list for steps and a bulleted list for parallel items.
  Use a table for numbers that are compared.
- Headings are noun phrases in sentence case, without puns or questions.
  If you change a heading in a Markdown file, update every link to its
  old anchor.
- A caption states what the image shows. It does not interpret it.

## Code and facts

- Do not change code, commands, file names, flags, URLs, YAML, JSON,
  HTML markup, CSS or JavaScript. Change only the prose around them.
- Do not change a fact, a number, a name or a claim. If a sentence cannot
  be rewritten without changing its meaning, leave the meaning and change
  the words.

## Before and after

| Before | After |
| --- | --- |
| Every carrier flies on autopilot. Take one and its departments become yours: what you leave on autopilot the machine keeps deciding; what you take, it asks you about, and falls to the default if you are slow. | Every carrier runs on autopilot. When you take a carrier, you choose which departments to run yourself. The simulator sends you a decision for each event in those departments. If you do not answer before the deadline, the simulator applies the default. |
| The scorecard is the same for everyone. An agent uses the same API. | The scorecard uses the same formula for every carrier. Agents use the same API as the page. |
| Nothing moves between systems except a message, and the globe draws only what the messages say. | Systems communicate only by messages. The globe shows only what the messages contain. |
| The lobby is as slow as the slowest peer, not the sum. | The lobby waits for the slowest peer. It does not wait for each peer in turn. |
| A quiet link is the world's to reap, not the edge's. | The core closes an idle link. Envoy does not. |
| The consoles are a window on the carriers' systems, not a door. | Anyone can read a carrier's console. Only the seat holder can change it. |
| Every one of these found real bugs in jetway, and each fix landed upstream with a regression test that was watched to fail first. | Each feature exposed bugs in jetway. Each fix is in jetway with a regression test. |
| What is still rough | Known limitations |
