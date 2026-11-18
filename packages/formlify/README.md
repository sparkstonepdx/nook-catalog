# Formlify

Forms for static sites on nook, with Netlify's markup, so a site moves between Netlify and nook unchanged.

## Using it
1. Install Formlify from the store.
2. In your site's `nook.json`, ask for it: `{"needs": ["formlify"]}`. Grant it on nook's Services page.
3. Mark your forms as you would for Netlify, and deploy:
   ```html
   <form name="contact" method="POST" netlify netlify-honeypot="bot-field" action="/thanks/">
     <p hidden><input name="bot-field"></p>
     <input type="email" name="email"> <textarea name="message"></textarea>
     <button>Send</button>
   </form>
   ```

## What happens
- **On each deploy:** nook hands Formlify the site's HTML. Each marked form loses its `netlify` attributes and gains a hidden `form-name`, as on Netlify; nothing else in the page changes. Forms inside comments, scripts, `<template>` and the like are left alone. The deploy log lists the forms found.
- **On a submission:** nook sends the site's POSTs to Formlify. It keeps a submission only if it names a form the site declared, keeps only the fields that form declares, drops it quietly if the honeypot is filled, allows 10 a minute per visitor, and takes up to 10 MB with up to 5 files.
- **Where it goes:** one JSON line per submission in `data/forms/HOST/FORM.jsonl`, files beside it. In your backups, readable with anything.
- **Telling you:** `NOTIFY_EMAIL` gets an email through the bound email service (Mail relay), with Reply-To set to the submitter's email field; `NOTIFY_WEBHOOK` gets the submission as JSON.
- **After submitting:** the visitor goes to the form's `action` page if it has one, or a small thank-you page. A script asking for JSON gets `{"ok": true}`.
- **Reading them:** Formlify's own address, behind nook's login: every form with its count, each form's submissions, a CSV download (cells that would run as a spreadsheet formula are quoted), files, and deleting one.

JavaScript-rendered forms work as on Netlify: keep a plain HTML copy of the form in a page so its fields are declared, and send `form-name` with the post.
