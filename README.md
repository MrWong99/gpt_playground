# Quick and dirty audio transcriptions and summary

This utilizes Google Cloud speech-to-text and OpenAI GPT-4 to create summaries for audio conversations.
Conversations can be provided from Discord via CraigBot.
Make sure to set required configs:

* __assistant.id__ `file` -> contains OpenAI assistant ID to send transcription to
* __bucketname__ `file` -> contains name of the Google Cloud storage bucket to store transcriptions in
* __openai.token__ `file` -> contains OpenAI API token for signing in
* __GOOGLE_APPLICATION_CREDENTIALS__ `env` -> path to a Google Cloud API sign in file
