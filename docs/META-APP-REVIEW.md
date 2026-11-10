# Meta App Review for Facebook

Do you need this page? Only if someone other than you will connect a Facebook
account to your polyemesis. If you stream to your own profile or your own Page,
skip it: you connect as a developer of your own Meta app and everything works at
once. See [Facebook Live (Meta)](PLATFORMS.md#facebook-live-meta) for that setup.

polyemesis is self-hosted, so there is no shared polyemesis Meta app. Every
operator registers their own app, and every operator who wants other people on
it goes through App Review themselves. Nobody can do it for you and nobody can
shorten it. This page is the checklist, the text to adapt and the recordings to
make.

Meta changes its dashboard and its wording often. What follows was checked
against Meta's documentation on 2026-11-09. Where the dashboard disagrees with
this page, the dashboard wins; each section names the source it came from.

## Who needs review

Meta's rule: if your app will be used by anyone without a role on the app (or a
role in a Business that has claimed it), the app must first go through App
Review. If everyone who uses it has a role, App Review is not required.
([App Review process](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review))

| Who connects | Review needed | What to do |
|---|---|---|
| You, on your own profile or Page | No | Add yourself as Administrator or Developer of the app. All permissions work in Development mode. |
| A co-streamer or colleague you trust | No | Add them under **App roles** as Developer or Tester. A Tester can be any Facebook user. Apps not linked to a Business allow up to 50 testers. |
| A client, a community or the public | **Yes** | Everything below, including Business Verification. |

Until the review is approved, a person with no role cannot grant your app its
permissions. Switching the app to Live early does not help: in Live mode an app
can only request approved permissions, and that applies to role users too, so
switching early can break your own connection.
([App modes](https://developers.facebook.com/documentation/development/build-and-test/app-modes))

## What polyemesis asks for

The permissions come from `Scopes()` in `internal/oauth/facebook.go`. One
sign-in asks for all of them; Facebook lets the person decline individual ones.

| Permission | Needs review | What polyemesis uses it for | Where you see it in the console |
|---|---|---|---|
| `public_profile` | No. Granted to every app | Reads the person's name and id to label the connected account. | **Settings, Platform credentials, Facebook**: the connected account's name. |
| `publish_video` | Yes (Advanced Access) | Goes live on a **personal profile**: creates a live video, hands polyemesis the ingest address, and ends the broadcast when you stop. | A Facebook destination linked to a profile account; **Start** and **Stop**. |
| `pages_show_list` | Yes (Advanced Access) | Lists the Pages the person manages, so they can choose one, and finds the Page's access token. | The Page picker after **Connect account**. |
| `pages_manage_posts` | Yes (Advanced Access) | Goes live on a **Page**: creates the Page's live video, edits its title and description, creates a poll on it, and ends it. | A Facebook destination linked to a Page; the destination's title and description fields; **Polls** panel; **Stop**. |
| `pages_read_engagement` | Yes (Advanced Access) | Reads the live video's comments into the chat pane and the reaction tally into the activity feed, and reads the Page and the live video's state. Meta also lists it as a dependency of `pages_manage_posts`. | The chat pane and the activity feed while a Page broadcast is live. |

Meta's own pages for each:

- `publish_video` allows an app to live-stream to a user's timeline, group,
  event or Page. It has no dependencies.
- `pages_show_list` allows an app to show a person the Pages they manage and to
  verify that they manage a Page. It has no dependencies.
- `pages_manage_posts` allows an app to publish, update and delete a post,
  photo or video on a Page. It depends on `pages_read_engagement` and
  `pages_show_list`.
- `pages_read_engagement` allows an app to read content posted by the Page and
  metadata about it. It depends on `pages_show_list`.

([Permissions reference](https://developers.facebook.com/docs/permissions);
[Live Video API requirements](https://developers.facebook.com/docs/live-video-api/):
publishing on a User needs `publish_video`, publishing on a Page needs
`pages_manage_posts` and `pages_read_engagement`.)

Because the three Page permissions depend on each other, request all three
together. If you only ever stream to profiles, request `publish_video` alone and
trim the others from your sign-in; if you only stream to Pages, you can leave
`publish_video` out of the review (polyemesis still asks for it, and Facebook
lets the person decline it).

polyemesis never posts text, photos or comments, never sends messages, and
never reads anything from a person's friends or timeline. Say so in your
descriptions: reviewers check that you ask for no more than you use.

## Standard and Advanced Access

Every permission has two levels. **Standard Access** works for people with a
role on your app. **Advanced Access** works for anyone, and you get it by
passing App Review. Advanced Access also requires Business Verification.
([Permissions reference](https://developers.facebook.com/docs/permissions),
Requirements; [Business Verification](https://developers.facebook.com/documentation/development/release/business-verification))

Request Advanced Access only for permissions you use. Meta tells you to request
only what the app needs, and a request for more is a common reason for
rejection.

## The steps

### 1. Make test calls, and keep them fresh

Meta will not let you click **Request advanced access** on a permission until
it has logged one successful API call that used it. The call must be within 30
days of the day you submit. The log can lag by up to two days, and the button
stays grey until it catches up. Calls can come from your app or from the Graph
API Explorer.
([Submission walkthrough](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/submission-guide),
Step 1)

With polyemesis, connect your account, then go live once on a Page and once on a
profile, with comments arriving. That makes calls with all five permissions.
Do this about a week before you plan to submit. If the 30 days run out, do it
again.

The **Testing** page under your use case shows the count per permission ("0 of 1
API call(s) required" until the log catches up).

### 2. Connect the app to a Business and verify it

Open **App settings, Basic, Verification** and connect the app to a Business you
administer, then finish Business Verification in Meta Business Manager. Apps
that request Advanced Access, or that let other Businesses use their data, must
be connected to a verified Business; until then people from other Businesses
cannot grant the permissions. Verification is done in Business Manager and by an
Admin of that Business, not by a developer on the app.
([Business Verification](https://developers.facebook.com/documentation/development/release/business-verification))

Meta asks for documents proving the Business exists. This is the step most
likely to take longer than the review itself, so start it first. It is also
where a hobbyist with no registered business gets stuck; see [Blockers](#what-can-stop-you).

### 3. Fill in the app settings

The review will not start until these are complete
([Submission walkthrough](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/submission-guide),
Step 3; [Basic settings](https://developers.facebook.com/documentation/development/create-an-app/app-dashboard/basic-settings)):

- [ ] **Display name** without "Facebook", "FB", "Face", "Book" or another Meta
      product name.
- [ ] **App icon**, 1024 by 1024 (512 to 1024 is accepted, under 5 MB, JPEG, GIF or
      PNG), with no Meta logos or trademarks and no "Facebook" or "FB" in it.
- [ ] **Contact email** you can read. Review notices go there.
- [ ] **Privacy Policy URL**, publicly reachable, covering your instance. See
      [the privacy policy](#the-privacy-policy-and-data-deletion).
- [ ] **Terms of Service URL**, required to switch to Live.
- [ ] **User data deletion**: either a callback URL or a page of instructions.
      See [the privacy policy](#the-privacy-policy-and-data-deletion).
- [ ] **Category** that describes the app (a video or streaming category).
- [ ] **App purpose**: **Yourself or your own business** if only people with a
      role will use it, **Clients** otherwise. For review, choose Clients.
- [ ] **Data Protection Officer contact**, if you do business in the European
      Union.
- [ ] The OAuth redirect URI `https://YOUR_HOST/api/v1/oauth/facebook/callback`
      in the Facebook Login settings, with Client and Web OAuth login on.

### 4. Answer the data handling questions

Once the Business is verified, the submission asks data handling questions
about what you do with the data the permissions return. Answers are evaluated
within about 30 seconds.
([Submission walkthrough](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/submission-guide),
Step 2; [Complete your submission](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/content))
Be accurate: the same answers come back in the yearly checkup.

### 5. Give the reviewer a way in

See [Reviewer access](#reviewer-access-to-a-self-hosted-instance). This is the
step where self-hosted apps most often fail.

### 6. Describe and record each permission

For each permission, write a use case description and upload a screen recording.
Each permission gets its own text: Meta says do not copy and paste one
description into all of them. Use the text and scripts below.

### 7. Submit, then wait

Click **Submit for Review** and accept the Platform Onboarding Terms. Meta says
you should have a decision within a week; in practice allow longer for a first
submission and for each resubmission.
([Submission walkthrough](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/submission-guide),
Step 6)

Changing the app's basic or advanced settings after you submit can send it back
for re-review. Finish the settings first.

### 8. Switch to Live, after approval

Only switch the app to Live once the review is approved. Data made in
Development mode, such as test broadcasts, becomes visible to everyone who uses
the app after the switch, so delete test posts first.
([App modes](https://developers.facebook.com/documentation/development/build-and-test/app-modes))
Anyone who connected before approval reconnects once from **Settings, Platform
credentials**, so the approved permissions apply to their login.

## About `business_management`

If your app uses the **Manage everything on your Page** use case, the dashboard
adds `business_management`, `pages_show_list` and `public_profile` to it and will
not let you remove them: Meta calls them required for that use case. Use cases
also cannot be removed from an app once added. Only `pages_manage_engagement`
(added by default) and optional permissions such as `pages_read_engagement` can
be removed.
([Create an app for the Pages API](https://developers.facebook.com/documentation/pages-api/create-an-app);
[Create an app](https://developers.facebook.com/documentation/development/create-an-app))

polyemesis does not use `business_management`, and its allowed usage (managing
ad accounts and other business assets) does not describe anything polyemesis
does. So:

- Do not request Advanced Access for it. You only request the permissions you
  need, and nothing in the polyemesis sign-in asks for it.
- Do not try to remove it from that use case; the dashboard refuses.
- If it blocks your submission, the way out is an app created without the
  **Manage everything on your Page** use case, taking the Page permissions
  through the **Access the Live Video API** use case instead. We have not
  verified that route end to end, because Meta's documentation does not list
  which permissions that use case offers. Try it in a new app, not your working
  one.

If the review form asks why `business_management` is present, the honest answer
is "added by default with the use case, not requested and not used".

## Use case descriptions

Meta asks: how does this permission help your users, why does the app need it,
how does the app use the data, and why would the app be less useful without it.
Be specific and describe what polyemesis does, not what the permission could
do. Replace the text in angle brackets. Write each one in your own words from
this base; reviewers see through identical text.

### Background paragraph, put at the top of each

> `<App name>` runs on `<https://your-host>`. It is a self-hosted live
> streaming control panel. A streamer connects their Facebook account to it,
> then starts a live broadcast to their profile or Page from one place,
> alongside other platforms. It is used by `<describe your users, for example
> the members of my streaming community>`.

### `public_profile`

Not submitted: it is granted to every app. If Meta lists it in the form, say:
"Reads the person's name and id to show which Facebook account is connected."

### `pages_show_list`

> After the person signs in with Facebook, the app asks which of their Pages
> should receive the live broadcast. It reads the list of Pages the person
> manages (GET /me/accounts, name, id, category) and shows it as a picker. It
> uses the same call to confirm the person manages the Page they choose. It
> reads nothing else about the Pages. Without this permission the app cannot
> show the person their Pages and cannot go live on one. This permission is
> also a dependency of `pages_manage_posts` and `pages_read_engagement`.

### `pages_manage_posts`

> The app lets a Page admin go live on their own Page. When the admin presses
> Start, the app creates a live video on the Page (POST /{page-id}/live_videos),
> sets its title and description, and returns the stream address to the
> admin's encoder. The admin can edit the title and description while live, can
> start a native poll on the live video (POST /{live-video-id}/polls) and close
> it, and ends the broadcast when they press Stop. The app does not publish
> text, photo or link posts and does not delete anything. Without this
> permission the app cannot create or end a live video on a Page.

Meta's checklist for this permission is written for text posts (create, edit,
delete a post). Your recording will show create, edit and end for a live
video, so say in the text that a Page live video is the post, and that the
poll and metadata edit are the edits.

### `pages_read_engagement`

> While the Page's live video is on air, the app reads its comments
> (GET /{live-video-id}/comments) and its reaction counts (GET
> /{live-video-id}/reactions) so the admin can read their audience in the
> app's chat pane and see a tally in the activity feed. It reads the Page and
> the live video's state to know when the broadcast is on air and when it has
> ended. The comments are shown to the admin only and are kept in the admin's
> own server. Without this permission the admin would stream blind, with no
> view of the Facebook audience, and the app could not confirm the broadcast.
> It is also a dependency of `pages_manage_posts`.

### `publish_video`

> The app lets a person go live on their own Facebook profile. When the person
> presses Start, the app creates a live video on their timeline (POST
> /me/live_videos), returns the stream address to the person's encoder, and
> ends the live video when the person presses Stop. The app does not post
> anything else and does not read the person's timeline. Without this
> permission the app cannot create a live video on a profile.

## Screen recordings

Meta wants one recording per permission, in 1080p or better, showing the whole
flow including the person granting the permission. Recordings are in English
where possible; add captions for anything the reviewer might not understand.
Do not blur or crop out the URL bar, the Facebook dialog or the Page. You may
reuse one long recording across the permissions if each permission's part is
clearly marked, but separate files are safer.
([Submission walkthrough](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/submission-guide),
Screen Recordings; Meta's per-permission checklists are in the
[Permissions reference](https://developers.facebook.com/docs/permissions).)

Meta's checklists for the Page permissions all begin with the full login,
showing the person granting the permission. Record the dialog, not just the
result.

Use a **Facebook account that is not your admin account** for the recording if
you can, with a Page it manages. Start from a state where the account is not yet
connected. Show the polyemesis address in the browser throughout.

### Opening shots, for every recording

1. The polyemesis sign-in page, then the dashboard, with the address bar visible.
2. **Settings, Platform credentials, Facebook**: show that the account is not
   connected, with no secrets on screen (blur the App Secret field if it is
   shown).
3. Press **Connect account**.
4. The Facebook dialog: pause on the permission list so the permissions can be
   read. Press Continue.
5. Back in polyemesis: the account is connected and shows the person's name.

### `pages_show_list`

1. The opening shots, ending on the Page picker listing the Pages.
2. Choose a Page. Show the connected account now naming that Page.
3. Say, on screen or in a caption: "The list comes from the permission; the app
   shows it and nothing else".

### `pages_manage_posts`

1. The opening shots with the Page chosen.
2. Open the Facebook destination for that Page. Show its title and
   description.
3. Start the destination (or press **Go live**). Show the encoder connecting.
4. Open the Page on facebook.com in another tab: the live video is on air with
   the title you set.
5. Change the title in polyemesis and show the title change on facebook.com.
6. Start a poll from the **Polls** panel with "also run as a native Facebook
   poll" on. Show the poll appearing on the live video on facebook.com. Close
   it.
7. Press **Stop**. Show the broadcast ending on facebook.com.

### `pages_read_engagement`

1. The opening shots and a Page broadcast on air, as above.
2. On facebook.com, from a second account, post a comment on the live video and
   add a reaction.
3. In polyemesis, show the comment arriving in the chat pane and the reaction
   count in the activity feed.
4. Stop the broadcast. Show the comment is still in the app's chat history.

### `publish_video`

1. The opening shots, signing in with an account and choosing the profile
   (not a Page).
2. Open the Facebook destination for the profile. Start it.
3. Open the profile on facebook.com: the live video is on air.
4. Press **Stop** and show it ending.

## Reviewer access to a self-hosted instance

Meta's reviewers test your app themselves. Their instructions: if they cannot
access your app, the whole submission is rejected; if they can test it but not
the functionality behind a permission, that permission is refused. The app must
be publicly available, or you must say how to access it. Reviewers use Meta's
own test accounts, so never put your own Facebook credentials in the form.
([App Review process](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review);
[Submission walkthrough](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/submission-guide),
Step 4)

For polyemesis, that means a reachable instance and a way to sign in to it.
polyemesis has **one admin and no roles**, so you cannot create a limited
reviewer account on your production server, and a signed-in session is full
control of that server's streaming (see [the FAQ](FAQ.md#is-there-a-multi-user-mode)).
Do not hand the reviewer your production admin password. Instead:

1. **Run a separate review instance.** A second install on its own hostname
   with a trusted certificate, a fresh data directory, and your Meta app's App
   ID and App Secret entered under **Settings, Platform credentials**. Add
   `https://review.example.com/api/v1/oauth/facebook/callback` to the app's
   redirect URIs. It holds no real destinations, keys or recordings.
2. **Give it its own admin password**, long and used nowhere else. That
   password is the "test credentials" you give Meta.
3. **Leave it running for the whole review**, including resubmissions. A
   reviewer who finds the server down rejects the submission.
4. **Do not add real stream keys.** The reviewer connects their own test
   Facebook account and its Page; the instance does not need to receive any
   video for Facebook to create the live video, though see the note below.
5. **When the review is decided, close access.** Run the server with `-reset-admin
   -revoke-api-tokens` on the review instance (this sets a new password, signs
   every session out and deletes every API token), disconnect the Facebook
   accounts, and then stop the instance or delete its data directory. If you
   keep the instance for later resubmission, keep it on a password the
   reviewer no longer has.

Because nothing on that instance is yours, there is nothing to protect there
except the Meta app secret. It is stored encrypted and is never returned by
the API, so a reviewer who signs in cannot read it. If you are still uneasy,
reset the app secret in your Meta app's **App settings, Basic** after the review and enter the new
one in your real instance.

Note on test video: the reviewer may want to see a picture on air. polyemesis
needs a source for that, so run an encoder (OBS, ffmpeg) into the review
instance yourself while the review is open, and say in the instructions that
you do and when. Without a source, what the reviewer sees on Facebook may not
show the broadcast going out.

### Reviewer instructions to paste

> Access: open `https://review.example.com`. Sign in with the password
> `<PASSWORD, entered in the form field, not here>`. The app is self-hosted
> software. This is a dedicated review instance with no other data.
>
> You will need a Facebook test account that manages a Page. To test the
> profile permission use the same account's profile.
>
> 1. Go to Settings, Platform credentials, Facebook, and press Connect
>    account. Grant all permissions in the Facebook dialog.
> 2. Choose your Page from the picker (this uses pages_show_list), or choose
>    your profile.
> 3. Open Destinations and add a Facebook destination for the account, then
>    press Start. A live video is created on your Page (pages_manage_posts) or
>    profile (publish_video).
> 4. Open the Page or profile on facebook.com to see it live. Comment on it
>    from another account.
> 5. In the app, open the chat pane: the comment appears there
>    (pages_read_engagement).
> 6. Open Polls, create a poll with "also run as a native Facebook poll" on,
>    and see it on the live video.
> 7. Press Stop. The live video ends.
>
> The app does not publish posts or messages, and does not read your
> friends or timeline.

Put the password only in the reviewer-instructions field, never in a use case
description or a recording. Button and panel names above are polyemesis's at
the time of writing; match them to your version.

## The privacy policy and data deletion

Meta shows your **Privacy Policy URL** to people on the Facebook dialog, and the
app must tell people how to ask for their data to be deleted. The **User Data
Deletion** field accepts either a callback URL that Meta calls when a person
removes the app, or a page of explicit instructions; it may be a section of the
privacy policy. If you use a callback it must be HTTPS and must answer with a
link and a confirmation number.
([Basic settings](https://developers.facebook.com/documentation/development/create-an-app/app-dashboard/basic-settings);
[Data deletion callback](https://developers.facebook.com/documentation/development/create-an-app/app-dashboard/data-deletion-callback))

polyemesis does not implement the callback, so use an **instructions page**. It
is your instance, so the policy is yours. The facts it has to state truthfully
are the ones polyemesis has: it stores the connected account's id and name, the
Page list, the access tokens (encrypted), the live video ids it creates, and
the chat comments and reaction tallies it reads, in your instance's own
database. Retention of chat is yours to
set; see [CHAT-EXPORT.md](CHAT-EXPORT.md). State who the controller is, how to
reach them, and how a person disconnects (remove the app in Facebook's Apps and
Websites settings, and ask you to delete the stored account and chat). We do
not provide policy text: write it for your own situation, and have it read by
someone who can advise you if you serve people in the EU.

## What the yearly checkup needs

An app with Advanced Access, or published live with a use case, must complete
a **Data Use Checkup** each year. You re-certify that the app follows allowed
usage and Meta's Platform Terms, answer data handling questions again and give
current testing instructions. Put the date in your calendar when you are
approved. Missing it can cost you API access.
([Data Use Checkup](https://developers.facebook.com/docs/resp-plat-initiatives/individual-processes/data-use-checkup))
So the review instance and its password have to outlive the approval, or you
need to be able to bring them back.

## Common reasons for rejection

From Meta's guidance and from what submissions commonly fail on:

- **The reviewer could not sign in or reach the app.** The whole submission is
  rejected. Test your review instance from a phone on mobile data first.
- **The recording does not show the login.** Meta's checklists start with the
  complete login and the person granting the permission.
- **The recording and the description disagree**, or the description is the
  same for every permission.
- **A permission is requested that the app does not use.** Request fewer.
- **No test call was logged** for a permission, or the calls are older than 30
  days.
- **The privacy policy URL is missing, unreachable or generic**, or the data
  deletion URL leads nowhere.
- **The app icon contains a Meta logo, or the name contains "Facebook".**
- **Settings were changed after submitting.**
- **The app is in Development mode when the reviewer tries it with a non-role
  account.** That is expected during review; follow Meta's instruction to
  leave it until approval.
- **The reviewer's account has no Page**, so the Page permissions could not be
  tested. Say in the instructions what the reviewer needs, and show the whole
  flow in your recording so the permission can still be judged.

## What can stop you

- **Business Verification.** If you have no registered business, you may not be
  able to complete it, and without it Advanced Access is not available. Then
  the supported setup is the one that needs no review: everyone who connects has
  a role on your app (up to 50 testers).
- **Facebook Login for Business.** If your app's login is set up as Facebook
  Login for Business, Meta's login flow there uses a configuration (a
  `config_id`) in place of the `scope` list that polyemesis sends. Whether
  polyemesis's scope-based sign-in works with such an app is [still being
  verified](CONNECTING-PLATFORMS.md#facebook-being-verified). Test your own
  sign-in before you submit: if it fails with an invalid-scope error, review
  cannot rescue it.
- **A reviewer cannot use a self-hosted instance.** Nothing we can do on our
  side; this is why the instructions have to be exact.

## Sources

- [App Review submission walkthrough](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/submission-guide)
- [App Review process](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review) and [Complete your App Review submission](https://developers.facebook.com/documentation/resp-plat-initiatives/individual-processes/app-review/content)
- [Permissions reference](https://developers.facebook.com/docs/permissions)
- [Live Video API](https://developers.facebook.com/docs/live-video-api/)
- [Business Verification](https://developers.facebook.com/documentation/development/release/business-verification)
- [Data Use Checkup](https://developers.facebook.com/docs/resp-plat-initiatives/individual-processes/data-use-checkup)
- [App modes](https://developers.facebook.com/documentation/development/build-and-test/app-modes) and [App roles](https://developers.facebook.com/documentation/development/build-and-test/app-roles)
- [Basic settings](https://developers.facebook.com/documentation/development/create-an-app/app-dashboard/basic-settings) and [Data deletion callback](https://developers.facebook.com/documentation/development/create-an-app/app-dashboard/data-deletion-callback)
- [Create an app](https://developers.facebook.com/documentation/development/create-an-app) and [Create an app for the Pages API](https://developers.facebook.com/documentation/pages-api/create-an-app)
- [Facebook Login for Business](https://developers.facebook.com/documentation/facebook-login/facebook-login-for-business)
