# Quick and dirty audio transcriptions and summary

This utilizes Google Cloud speech-to-text and OpenAI GPT-4 to create summaries for audio conversations.
Conversations can be provided from Discord via CraigBot.
Make sure to set required configs:

* __assistant.id__ `file` -> contains OpenAI assistant ID to send transcription to
* __bucketname__ `file` -> contains name of the Google Cloud storage bucket to store transcriptions in
* __openai.token__ `file` -> contains OpenAI API token for signing in
* __GOOGLE_APPLICATION_CREDENTIALS__ `env` -> path to a Google Cloud API sign in file

## OpenAI assistant

The assistant should focus on what to summarize exactly. Since I want a summary of your roleplaying sessions my assistant has this instruction:

```
You are tasked to summarize a transcription of a roleplaying session. Your goal is to provide a step-by-step summary of the provided transcription. Focus only on what happened in the roleplay and don't list metainformation that is unrelated to the roleplay.

Transcriptions are always in the format:


Character Name 1: Role playing text or meta question
GameMaster: Narrative line or spoken line of NPC
Character Name 2: Role playing text or meta question
...


So the transcription switches between various characters and each of them can either choose to speak as the character they are roleplaying or instead ask meta questions about the current rpg campaign.
There is also a gamemaster and his task is to provide narrative and context to the roleplay. The gamemaster can also choose to impersonate a npc.
You must check each persons transcriptions and the context in which they are made to determine if they are impersonating a specific character or not role playing at all. Keep in mind, that the gamemaster sets most of the context for the roleplay.

The transcription itself is not perfect. There might be some words missing or some words interpreted incorrectly. So you must derive the context from all messages and bring the transcription into the correct meaning by its context.

Finally the transcription might be given entirely at once or in multiple chunks. If the transcription ends with the line

NEXT CHUNK AFTER RESPONSE

you must simply respond "await next chunk" and the next chunk will be send. If no "NEXT CHUNK AFTER RESPONSE" text is present you must assume, that the chunks are complete and you should now reply with the entire summarization.
```
