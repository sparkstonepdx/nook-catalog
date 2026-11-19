# Forms

Make forms and collect answers, like Google Forms, on a nook server. Install it from the store.

## Using it
1. Open Forms (behind your nook login) and press **New form**.
2. Write the title, a description, and the questions. It saves as you go.
3. Turn on **Taking responses** and copy the link. Anyone with it can answer; nothing else is public.
4. **Responses** shows a summary per question (counts for choices, the latest answers for the rest), each response in a table, and a CSV download.

## Questions
Short answer, paragraph, multiple choice, checkboxes, dropdown, email, number and date. Each can be required and have help text. Questions move up and down, duplicate, and delete; options are added with Enter.

## Sections
**Add a section** starts a new page with its own title and description. Under each section, **After section N** continues to the next one, goes to another, or submits. A multiple choice or dropdown question can instead send people to a section by their answer (**Go to section by answer**). People can go **Back**; answers on pages they skipped aren't kept. It all works without JavaScript: the pages they went through and their answers so far ride along in the page, and are checked again before the response is kept.

## What it checks
- **Answers:** each against its question: an option that's offered, an email that looks like one, a number, a date. A wrong or missing answer comes back beside its question with what was typed kept.
- **Spam:** a hidden field only bots fill in drops a response quietly; each address may send 10 a minute.
- **Size:** up to 100 questions, 50 options each, 10,000 characters an answer, 1 MB a response.
- **CSV:** a cell that would run as a spreadsheet formula is quoted.
- **The editor:** posts from another site with your login are refused.

## Telling you
Each optional:
- **Email:** `NOTIFY_EMAIL` gets each response through the bound email service (Mail relay), from `NOTIFY_FROM`. If the form has an email question, replying answers the person.
- **Push:** bind Forms to ntfy's notify service and subscribe to the `forms` topic.

## Where it keeps things
`data/forms/ID/form.json` and `responses.jsonl`, one response a line. In your backups, readable with anything.

The form people answer is plain HTML and works without JavaScript; the editor needs it.
