#!/usr/bin/env python3
"""Generates the automod fixture corpus.

    python3 -I gen_corpus.py            # rewrites corpus-en.jsonl and corpus-en.positive.jsonl

Stdlib only and fully deterministic (fixed seeds), so a regeneration that
changes a line shows up in review. Offensive positives are never written out by
hand: they are built from the embedded pack's own terms (../en-v1.json) plus
obfuscation variants. Everything hand-written here is benign or a trap.

Doxxing fixtures use reserved or fake data only: 555-01xx phone numbers,
example.* and .test email domains, and the RFC 5737 / RFC 3849 documentation
ranges for IPs.
"""
import json
import os
import random
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PACK = os.path.join(HERE, "..", "en-v1.json")

MAIN = []  # corpus-en.jsonl
POS = []  # corpus-en.positive.jsonl (slur positives only)
SEEN = set()


def add(dst, text, category, expect, note, slice_, group="", **kw):
    key = (text, kw.get("author", ""), kw.get("platform", ""))
    if key in SEEN:
        return False
    SEEN.add(key)
    row = {"text": text, "category": category, "group": group, "expect": expect, "note": note, "slice": slice_}
    for k, v in kw.items():
        if v not in ("", None):
            row[k] = v
    dst.append(row)
    return True


def pick(rng, seq):
    return seq[rng.randrange(len(seq))]


# ---------------------------------------------------------------- pack terms
pack = json.load(open(PACK, encoding="utf-8"))
SEVERE = sorted(e["term"] for e in pack["entries"] if e["category"] == "hate_speech" and e["tier"] == "severe")
CONTEXTUAL = sorted(e["term"] for e in pack["entries"] if e["category"] == "hate_speech" and e["tier"] == "contextual")
EXCEPTS = sorted({x for e in pack["entries"] for x in e["except"]})

# ------------------------------------------------------------ obfuscation kit
LEET = {"a": "4", "e": "3", "i": "1", "o": "0", "s": "5", "t": "7"}
LEET2 = {"a": "@", "s": "$"}
HOMO = {"a": "а", "e": "е", "o": "о", "p": "р", "c": "с", "x": "х", "y": "у", "i": "і"}
ZWSP = "​"


def fullwidth(s):
    return "".join(chr(ord(c) + 0xFEE0) if "a" <= c <= "z" else c for c in s)


def leetify(t, rng, table):
    chars = list(t)
    idx = [i for i, c in enumerate(chars) if c in table]
    if len(idx) < 1:
        return None
    # replace roughly half, at least one
    k = max(1, (len(idx) + 1) // 2)
    for i in rng.sample(idx, k):
        chars[i] = table[chars[i]]
    return "".join(chars)


def homoglyph(t, rng):
    chars = list(t)
    idx = [i for i, c in enumerate(chars) if c in HOMO]
    if not idx:
        return None
    k = max(1, (len(idx) + 1) // 2)
    for i in rng.sample(idx, k):
        chars[i] = HOMO[chars[i]]
    return "".join(chars)


def spaced(t):
    letters = [c for c in t if c != " "]
    if len(letters) < 4 or " " in t:
        return None
    return " ".join(letters)


def zwsp(t, rng):
    out = []
    for c in t:
        out.append(c)
        if c != " " and rng.random() < 0.5:
            out.append(ZWSP)
    s = "".join(out)
    return s if ZWSP in s else t[0] + ZWSP + t[1:]


def repeat(t, rng):
    # The pack form collapses runs to two, so only a letter the stored term
    # already doubles survives being stretched.
    idx = [i for i in range(len(t) - 1) if t[i].isalpha() and t[i] == t[i + 1]]
    if not idx:
        return None
    i = pick(rng, idx)
    return t[: i + 1] + t[i] * rng.randrange(3, 6) + t[i + 1 :]


HOSTILE = [
    "you are such a {t}",
    "nobody wants you here {t}",
    "get out of this chat {t}",
    "shut up {t}",
    "what a {t} take",
    "{t} go away",
    "typical {t} behaviour",
    "stop posting {t}",
    "hey {t} you are banned in my head",
    "imagine being a {t} lol",
    "all of you are {t}",
    "only a {t} would say that",
]

# ===================================================================== HATE
def gen_hate():
    rng = random.Random(1001)
    # severe, plain
    for i, t in enumerate(SEVERE):
        if i % 2 == 0 or " " in t:
            add(POS, HOSTILE[i % len(HOSTILE)].format(t=t), "hate_speech", "hold",
                "severe tier term in plain use", "positive", "hate_speech/severe", tier="severe")
    # severe, obfuscated
    kinds = [
        ("leet", lambda t: leetify(t, rng, LEET)),
        ("leet", lambda t: leetify(t, rng, {**LEET, **LEET2})),
        ("homoglyph", lambda t: homoglyph(t, rng)),
        ("spaced", spaced),
        ("zwsp", lambda t: zwsp(t, rng)),
        ("repeat", lambda t: repeat(t, rng)),
        ("fullwidth", lambda t: fullwidth(t)),
    ]
    notes = {
        "leet": "severe term with digit or symbol substitution",
        "homoglyph": "severe term with Cyrillic lookalike letters (UTS #39 skeleton)",
        "spaced": "severe term spelled out letter by letter",
        "zwsp": "severe term with zero-width characters inserted",
        "repeat": "severe term with a stretched letter",
        "fullwidth": "severe term in fullwidth forms (NFKC)",
    }
    n = 0
    for pass_ in range(3):
        for i, t in enumerate(SEVERE):
            if len(t) < 4:
                continue
            kname, fn = kinds[(i + pass_ * 3) % len(kinds)]
            v = fn(t)
            if v is None or v == t:
                continue
            if kname == "spaced":
                text = v
                if pass_ == 1:
                    text = v + " !!!"
            else:
                text = HOSTILE[(i + pass_) % len(HOSTILE)].format(t=v)
            if add(POS, text, "hate_speech", "hold", notes[kname], "positive", "hate_speech/severe",
                   tier="severe", obf=kname):
                n += 1
        if n >= 112:
            break
    # mixed
    for i, t in enumerate(SEVERE):
        if len(t) < 5 or " " in t or i % 3:
            continue
        a = leetify(t, rng, LEET)
        if a is None:
            continue
        b = zwsp(a, rng)
        add(POS, HOSTILE[i % len(HOSTILE)].format(t=b), "hate_speech", "hold",
            "severe term, leet and zero-width combined", "positive", "hate_speech/severe", tier="severe", obf="mixed")
    # contextual, plain
    ctx_tpl = [
        "that guy is a {t}",
        "i can't believe he called her a {t}",
        "{t} energy honestly",
        "the {t} in chat again",
        "such a {t} move",
        "{t} lol",
    ]
    for i, t in enumerate(CONTEXTUAL):
        add(POS, ctx_tpl[i % len(ctx_tpl)].format(t=t), "hate_speech", "flag",
            "contextual tier term: flag only, Jev settles it", "positive", "hate_speech/contextual", tier="contextual")
    # contextual, obfuscated
    for i, t in enumerate(CONTEXTUAL):
        if len(t) < 5 or i % 8:
            continue
        v = homoglyph(t, rng) if i % 16 else leetify(t, rng, LEET)
        if v is None or v == t:
            continue
        add(POS, ctx_tpl[(i + 1) % len(ctx_tpl)].format(t=v), "hate_speech", "flag",
            "contextual tier term, obfuscated", "positive", "hate_speech/contextual", tier="contextual",
            obf="homoglyph" if i % 16 else "leet")


# =================================================================== HARDCASE
SIGNALS = ["contextual_hate", "address_violence", "obfuscation_residue", "self_harm", "sexual_address", "insult_reply"]

HC_ADDR_VIOL = [
    ("i will find you and hurt you", "threat", "pass", "threat aimed at a person"),
    ("u better watch out i know where you live", "threat", "pass", "implied threat"),
    ("ur going to die for that", "threat", "pass", "threat in plain words"),
    ("@dusk_runner i'm going to shoot you if you talk again", "threat", "pass", "addressed threat"),
    ("someone should swat you", "threat", "pass", "swatting wish"),
    ("you won't be alive next week", "threat", "pass", "threat"),
    ("I'll hurt you where you stand", "threat", "pass", "threat"),
    ("keep talking and i will end you irl", "threat", "pass", "threat with irl marker"),
    ("hey u, i'm gonna find you after the stream", "threat", "pass", "stalking-flavoured threat"),
    ("@marble_owl you are going to get hurt", "threat", "pass", "addressed threat"),
    ("you should kill the boss with fire first", "none", "pass", "gaming advice with a violence word"),
    ("ur gonna die in this level lol the spikes are brutal", "none", "pass", "gaming prediction"),
    ("you can shoot the barrels to clear the room", "none", "pass", "gaming advice"),
    ("i'll shoot you the link in a sec", "none", "pass", "figurative shoot"),
    ("@pixel_fern you will die three times on this jump", "none", "pass", "gaming banter"),
    ("u gonna kill it at the finals", "none", "pass", "praise, kill as success"),
    ("you hurt my feelings with that clutch lol", "none", "pass", "joke"),
    ("did you find you key yet", "none", "pass", "gaming chat, typo"),
    ("ur killing it tonight", "none", "pass", "praise"),
    ("you shot that perfectly", "none", "pass", "gaming praise"),
    ("i can't find you on the map", "none", "pass", "gaming chat"),
    ("you could die from hunger in this mode", "none", "pass", "game mechanic"),
    ("@oak_signal thank you, you saved me from the swat team in that mission", "none", "pass", "game mission"),
    ("ur chat is going to kill me with laughter", "none", "pass", "joke"),
    ("i'd hurt you in a fair duel", "none", "pass", "playful challenge"),
]
HC_OBF = [
    ("h3ll0 fr13nd5 w3lc0m3 t0 th3 str34m", "none", "benign leetspeak greeting"),
    ("gr34t g4m3 t0n1ght", "none", "benign leetspeak"),
    ("y0u 4r3 w0rthl355 4nd n0b0dy l1k35 y0u", "harassment", "leetspeak insult"),
    ("th4t w45 th3 b35t cl1p 3v3r", "none", "benign leetspeak"),
    ("h4ck3r m0d3 4ct1v4t3d", "none", "gaming leetspeak"),
    ("yоu аre а clоwn", "harassment", "Cyrillic lookalikes in a mild insult"),
    ("nіce plаy thаt wаs hуpe", "none", "Cyrillic lookalikes, friendly"),
    ("w3lc0m3 b4ck 3v3ry0n3", "none", "benign leetspeak"),
    ("g00d m0rn1ng ch4t", "none", "benign leetspeak"),
    ("5t0p pl4y1ng l1k3 th15 y0u 1d10t", "harassment", "leetspeak insult"),
    ("lеt's gо", "none", "Cyrillic lookalikes, friendly"),
    ("y0ur 4im 1s tr4sh m4t3", "none", "leetspeak banter"),
    ("n0 w4y th4t just h4pp3n3d", "none", "benign leetspeak"),
    ("c0ngr4ts 0n th3 w1n", "none", "benign leetspeak"),
    ("sаy hеllo tо mе", "none", "Cyrillic lookalikes, friendly"),
    ("h0w d1d y0u g3t th4t 5k1n", "none", "benign leetspeak"),
    ("g3t 0ff my l4wn y0u cl0wn", "harassment", "leetspeak insult"),
    ("f4k3 5t4ts th15 ch4nn3l", "spam", "leetspeak accusation"),
    ("r34lly n1c3 r0und", "none", "benign leetspeak"),
    ("y0u 5uck at th15 g4m3", "harassment", "leetspeak insult"),
    ("tаke the L", "none", "Cyrillic a in slang"),
    ("5ub5 t0 th3 5tr34m3r", "spam", "leetspeak sub-begging"),
    ("th4nk5 f0r th3 f0ll0w", "none", "benign leetspeak"),
    ("c0m3 d0wn y0u 5t0p1d cl0wn", "harassment", "leetspeak insult"),
    ("pl4y 5ome m0r3 mu51c", "none", "benign leetspeak"),
]
HC_SELF = [
    ("kys", "self_harm", "bare 'kys', aimed or not"),
    ("@dusk_runner kys", "self_harm", "kys aimed at someone"),
    ("kms lol that boss again", "none", "gaming hyperbole, not a self-harm signal"),
    ("i want to die", "self_harm", "stated wish"),
    ("i just want to end it", "self_harm", "stated wish"),
    ("honestly i want to die after that loss lol", "none", "gaming hyperbole"),
    ("kms i missed again", "none", "gaming hyperbole"),
    ("you should just kys", "self_harm", "encouraging self-harm"),
    ("i think about cutting myself sometimes", "self_harm", "disclosure"),
    ("i'm going to end it tonight", "self_harm", "stated intent"),
    ("kys nerd", "self_harm", "kys as an insult"),
    ("this lag makes me want to die", "none", "hyperbole"),
    ("i feel like ending it all", "self_harm", "stated wish"),
    ("kms if i lose this round", "none", "hyperbole"),
    ("nobody would miss me if i left", "self_harm", "hopelessness"),
    ("go kys @pixel_fern", "self_harm", "kys aimed at someone"),
    ("lol i could kms over this ping", "none", "hyperbole"),
    ("i want to die of embarrassment", "none", "figurative"),
    ("i can't do this anymore, i want out for good", "self_harm", "stated wish, ambiguous"),
    ("kys is a ban word here right?", "none", "meta discussion of the word"),
    ("my cat just knocked the router, kms", "none", "hyperbole"),
    ("i cut myself shaving this morning lol", "none", "benign literal"),
    ("end it already, this boss fight is too long", "none", "benign 'end it'"),
    ("someone tell @marble_owl to kys", "self_harm", "kys aimed at someone"),
    ("i don't want to be alive anymore", "self_harm", "stated wish"),
]
HC_SEX = [
    ("you look hot today", "sexual_content", "mild remark aimed at a person"),
    ("@dusk_runner you sound sexy in that voice", "sexual_content", "remark aimed at a person"),
    ("send me a pic of you", "sexual_content", "solicitation"),
    ("are you single, you are so hot", "sexual_content", "remark aimed at a person"),
    ("that's a hot take", "none", "benign 'hot'"),
    ("the pizza is hot, careful", "none", "benign 'hot'"),
    ("you are so hot at this game, 20 kills", "none", "'hot' as skilled"),
    ("@pixel_fern nice stream, you're hot streak continues", "none", "hot streak"),
    ("show us more of you", "sexual_content", "suggestive request"),
    ("do you have a boyfriend, you're gorgeous", "sexual_content", "remark aimed at a person"),
    ("ur so sexy when you win", "sexual_content", "remark aimed at a person"),
    ("your setup looks so sexy ngl", "none", "setup, not a person"),
    ("the new skin is sexy", "none", "item, not a person"),
    ("you are the hottest streamer i know lol", "sexual_content", "remark aimed at a person"),
    ("dm me a pic u", "sexual_content", "solicitation"),
    ("u should wear less on stream", "sexual_content", "suggestive request"),
    ("this soundtrack is sexy", "none", "music"),
    ("@oak_signal you are cute", "none", "mild compliment"),
    ("you are adorable when you laugh", "none", "mild compliment"),
    ("ur voice is hot", "sexual_content", "remark aimed at a person"),
    ("that was a spicy play from you", "none", "'spicy' as skilled"),
    ("hot damn you clutched that", "none", "exclamation"),
    ("you are so hot i can't focus on the game", "sexual_content", "remark aimed at a person"),
    ("u have a hot mic btw", "none", "'hot mic' warning"),
    ("sexy aim you got there", "none", "'sexy' as skilled"),
]
HC_INSULT = [
    ("@dusk_runner you absolute muppet", "none", "friendly banter between regulars"),
    ("@marble_owl you're an idiot and everyone knows it", "harassment", "addressed insult"),
    ("@pixel_fern nice aim, clown", "none", "playful ribbing"),
    ("@oak_signal you are useless, log off", "harassment", "addressed insult"),
    ("@dusk_runner skill issue lol", "none", "gaming ribbing"),
    ("@marble_owl nobody asked loser", "harassment", "addressed insult"),
    ("@pixel_fern you donut, that was my kill", "none", "friendly banter"),
    ("@oak_signal you're pathetic", "harassment", "addressed insult"),
    ("@dusk_runner what a noob move, love it", "none", "friendly banter"),
    ("@marble_owl you're trash and so is your channel", "harassment", "addressed insult"),
    ("@pixel_fern ur an idiot for leaving that chest", "none", "ribbing"),
    ("@oak_signal shut up, nobody cares what you think", "harassment", "addressed insult"),
    ("@dusk_runner you madlad, that was insane", "none", "praise"),
    ("@marble_owl stupid take as always", "harassment", "addressed insult"),
    ("@pixel_fern you clown, I'm dying, that was so funny", "none", "laughing with"),
    ("@oak_signal what a loser, you quit again", "harassment", "addressed insult"),
    ("@dusk_runner lol you walnut", "none", "affectionate insult"),
    ("@marble_owl get a life, pathetic", "harassment", "addressed insult"),
    ("@pixel_fern ur so dumb lol (love you)", "none", "affectionate"),
    ("@oak_signal everyone here thinks you're a joke", "harassment", "addressed insult"),
    ("@dusk_runner bro you're so bad at this and i love it", "none", "affectionate"),
    ("@marble_owl you are the worst mod ever", "harassment", "addressed insult"),
    ("@pixel_fern you menace, stop sniping me", "none", "playful"),
    ("@oak_signal useless idiot", "harassment", "addressed insult"),
    ("@dusk_runner ok boomer lol", "none", "ribbing"),
]
HC_CTX_HOSTILE = [
    "you people are all {t} honestly",
    "@dusk_runner you absolute {t}",
    "get lost {t}",
    "what kind of {t} says that",
]
HC_CTX_BENIGN = [
    "the word {t} came up in a linguistics lecture today",
    "my professor wrote {t} on the board as an example of a reclaimed word",
    "can we talk about why {t} is on the filter list",
    "in the film they say {t} but it's a quote",
    "{t} is what the character gets called in chapter two",
]


def gen_hardcase():
    rng = random.Random(1002)
    ctx = rng.sample(CONTEXTUAL, 25)
    for i, t in enumerate(ctx):
        if i < 12:
            text = HC_CTX_HOSTILE[i % len(HC_CTX_HOSTILE)].format(t=t)
            jev, note = "hate_speech", "contextual term used against a person; pack flags, Jev should hold"
        else:
            text = HC_CTX_BENIGN[i % len(HC_CTX_BENIGN)].format(t=t)
            jev, note = "none", "contextual term mentioned, not used; pack flags, Jev should clear"
        add(POS, text, "hate_speech", "flag", note, "hardcase", "hate_speech/contextual",
            tier="contextual", signal="contextual_hate", jev=jev)
    for sig, rows in (("address_violence", HC_ADDR_VIOL),):
        for text, jev, exp, note in rows:
            add(MAIN, text, "none", exp, note + "; pack alone passes, Jev decides", "hardcase", signal=sig, jev=jev)
    for text, jev, note in HC_OBF:
        add(MAIN, text, "none", "pass", note + "; obfuscation residue with no pack term", "hardcase",
            signal="obfuscation_residue", jev=jev, esc=any(ord(c) > 127 for c in text))
    for text, jev, note in HC_SELF:
        add(MAIN, text, "none", "pass", note + "; pack alone passes, Jev decides", "hardcase", signal="self_harm", jev=jev)
    for text, jev, note in HC_SEX:
        add(MAIN, text, "none", "pass", note + "; pack alone passes, Jev decides", "hardcase", signal="sexual_address", jev=jev)
    for text, jev, note in HC_INSULT:
        add(MAIN, text, "none", "pass", note + "; pack alone passes, Jev decides", "hardcase", signal="insult_reply", jev=jev)


# ================================================================== DOXXING
FAKE_NAMES = ["Jordan Example", "Alex Sample", "Sam Placeholder", "Riley Testcase", "Casey Fixture", "Morgan Dummy", "Taylor Mockson", "Jamie Notreal"]
EXAMPLE_DOMAINS = ["example.com", "example.org", "example.net", "mail.example.com", "corp.example", "inbox.test", "mail.test", "fixture.test"]
LOCALS = ["jordan.example", "alex_sample", "sam.placeholder", "riley.test", "casey_fixture", "morgan.dummy", "taylor.mockson", "jamie.notreal",
          "fixture.user", "test.person", "demo_account", "no.reply.fake", "sample.sender", "j.doe.fake", "a.tester"]
PHONE_FRAME = ["call {n} at {p}", "his number is {p}", "her phone: {p}", "{p} that's where {n} lives, call", "text {n} on {p}",
               "dox: {n} {p}", "here is {n}'s cell {p}", "{p}", "ring {p} and ask for {n}", "number for {n}: {p}"]
EMAIL_FRAME = ["email {n}: {e}", "his email is {e}", "write to {e}", "{e}", "dox: {n} {e}", "contact {n} at {e}", "send it to {e} lol", "her address {e}"]
IP_FRAME = ["his ip is {i}", "{i}", "dox: {n} {i}", "ddos {i}", "that's {n}'s ip: {i}", "found it, {i}", "ip of the guy: {i}", "{n} lives at {i}"]


def phones(rng):
    """Every phone fixture ends in the fiction range 555-01xx, so one rule audits them all."""
    out = []
    npa = ["202", "212", "303", "415", "512", "617", "702", "818", "206", "312"]
    for i in range(14):
        a = npa[i % len(npa)]
        last = "01%02d" % rng.randrange(0, 100)
        forms = [
            "(%s) 555-%s" % (a, last),
            "%s-555-%s" % (a, last),
            "%s.555.%s" % (a, last),
            "%s 555 %s" % (a, last),
            "+1 %s 555 %s" % (a, last),
            "+1-%s-555-%s" % (a, last),
            "1-%s-555-%s" % (a, last),
        ]
        out.append((forms[i % len(forms)], "NANP shape, 555-01xx fiction range"))
    intl = [
        ("+44 20", "UK"), ("+44 7700", "UK mobile"), ("+49 30", "German"), ("+49 151", "German mobile"), ("+33 1", "French"), ("+33 6", "French mobile"),
        ("+81 3", "Japanese"), ("+81 90", "Japanese mobile"), ("+61 2", "Australian"), ("+61 4", "Australian mobile"), ("+55 11", "Brazilian"),
        ("+55 11 9", "Brazilian mobile"), ("+91 98", "Indian mobile"), ("+91 22", "Indian"), ("+34 91", "Spanish"), ("+39 06", "Italian"), ("+31 20", "Dutch"),
        ("+46 8", "Swedish"), ("+48 22", "Polish"), ("+90 212", "Turkish"), ("+52 55", "Mexican"), ("+353 1", "Irish"), ("+82 2", "Korean"),
        ("+27 11", "South African"), ("+62 21", "Indonesian"), ("+66 2", "Thai"), ("+84 24", "Vietnamese"), ("+7 495", "Russian"), ("+380 44", "Ukrainian"),
        ("+41 44", "Swiss"),
    ]
    for i, (cc, kind) in enumerate(intl):
        out.append(("%s 555 01%02d" % (cc, 40 + i), "E.164 shape, %s" % kind))
    out += [
        ("+1 (202) 555-0172", "E.164 with brackets"),
        ("+44 (0)20 555 0173", "E.164 with bracketed zero"),
        ("+49.30.555.0174", "E.164 with dots"),
        ("+33-1-55-50-01-75".replace("55-50-01-75", "555-0175"), "E.164 with dashes"),
        ("+12025550176", "E.164 unseparated"),
        ("+49 30 555 0177", "E.164 with spaces"),
        ("202 555 0178", "10 digits, two separators"),
        ("1 202 555 0179", "11 digits, three separators"),
        ("202/555/0180", "10 digits, slash separators"),
    ]
    return out


def gen_doxxing():
    rng = random.Random(2001)
    for i, (p, note) in enumerate(phones(rng)):
        frame = PHONE_FRAME[i % len(PHONE_FRAME)]
        text = frame.format(n=FAKE_NAMES[i % len(FAKE_NAMES)], p=p)
        add(MAIN, text, "doxxing", "hold", "phone number, %s" % note, "positive", "doxxing/phone", detector="phone")
    # email
    for i in range(24):
        e = "%s@%s" % (LOCALS[i % len(LOCALS)], EXAMPLE_DOMAINS[(i * 3) % len(EXAMPLE_DOMAINS)])
        if i % 5 == 1:
            e = e.replace("@", "+tag%d@" % i, 1) if False else e
        text = EMAIL_FRAME[i % len(EMAIL_FRAME)].format(n=FAKE_NAMES[i % len(FAKE_NAMES)], e=e)
        add(MAIN, text, "doxxing", "hold", "email address on a reserved domain", "positive", "doxxing/email", detector="email")
    pairs = [("example", "com"), ("example", "org"), ("example", "net"), ("fixture", "test"), ("mail.example", "com")]
    for i in range(14):
        loc = LOCALS[(i * 2) % len(LOCALS)].replace(".", " ").replace("_", " ").split()[0]
        dom, tld = pairs[i % len(pairs)]
        if i % 2:
            e = "%s [at] %s [dot] %s" % (loc, dom, tld)
            note = "obfuscated email with [at]/[dot]"
        else:
            e = "%s (at) %s (dot) %s" % (loc, dom, tld)
            note = "obfuscated email with (at)/(dot)"
        text = EMAIL_FRAME[(i + 2) % len(EMAIL_FRAME)].format(n=FAKE_NAMES[i % len(FAKE_NAMES)], e=e)
        add(MAIN, text, "doxxing", "hold", note, "positive", "doxxing/email", detector="email", obf="spelled")
    # IPv4 on the documentation ranges
    nets = ["192.0.2", "198.51.100", "203.0.113"]
    for i in range(30):
        ip = "%s.%d" % (nets[i % 3], 1 + (i * 7) % 253)
        text = IP_FRAME[i % len(IP_FRAME)].format(n=FAKE_NAMES[i % len(FAKE_NAMES)], i=ip)
        add(MAIN, text, "doxxing", "hold",
            "IPv4 on an RFC 5737 documentation range; the detector must not reject these in corpus runs", "positive",
            "doxxing/ipv4", detector="ipv4")
    # IPv6 on 2001:db8::/32
    v6 = [
        "2001:db8:85a3:0:0:8a2e:370:7334", "2001:db8::8a2e:370:7334", "2001:db8:1234:5678:9abc:def0:1234:5678",
        "2001:db8:0:0:0:0:0:1", "2001:db8::1:2:3", "2001:db8:aaaa:bbbb:cccc:dddd:eeee:ffff", "2001:db8:1::ab9:C0A8:102",
        "2001:db8:3333:4444:5555:6666:7777:8888", "2001:db8::ff00:42:8329", "2001:db8:abcd:12::5", "2001:db8:10:20:30:40:50:60",
        "2001:db8:dead:beef::1", "2001:db8:cafe:f00d:1:2:3:4", "2001:db8::a:b:c:d", "2001:db8:0:1:0:2:0:3",
        "2001:db8:f00:ba4::9", "2001:db8:1111:2222:3333:4444:5555:6666", "2001:db8:ab::cd:ef:12",
        "2001:db8:5:6:7:8:9:a", "2001:db8:b0b:cafe::42",
    ]
    for i, a in enumerate(v6):
        text = IP_FRAME[(i + 1) % len(IP_FRAME)].format(n=FAKE_NAMES[i % len(FAKE_NAMES)], i=a)
        add(MAIN, text, "doxxing", "hold", "IPv6 on the RFC 3849 documentation range", "positive", "doxxing/ipv6", detector="ipv6")


# ============================================================= NUMBER TRAPS
def gen_number_traps():
    rng = random.Random(2002)
    G = "doxxing/phone"
    scores = ["3-2", "21-19", "7-0", "108-102", "4-1", "15-13", "2-2", "10-8", "99-97", "6-4", "25-23", "1-0", "120-118", "11-9", "5-5"]
    s_frames = ["final score {s}", "we lost {s} lol", "it's {s} in the 3rd game", "{s} !!", "up {s} in round 2", "that sweep ended {s}",
                "score update: {s}", "game point, {s}"]
    for sc in scores:
        for f in (s_frames[hash_i(sc) % len(s_frames)], s_frames[(hash_i(sc) + 3) % len(s_frames)]):
            add(MAIN, f.format(s=sc), "doxxing", "pass", "score, not a phone number", "number_trap", G)
    times = ["12:30:45", "3:45", "10:15", "23:59:59", "00:42:17", "1:02:33", "9:05", "17:30", "2:15:08", "11:11:11", "0:59", "6:30:00"]
    t_frames = ["clip is at {t} in the vod", "stream starts at {t}", "boss dies at {t}", "jump to {t}", "timestamp {t}", "speedrun time {t}", "we went live at {t} est"]
    for t in times:
        for k in (0, 3):
            add(MAIN, t_frames[(hash_i(t) + k) % len(t_frames)].format(t=t), "doxxing", "pass", "time or timestamp", "number_trap", G)
    dates = ["2026-11-07", "2025-01-31", "2024-12-25", "2026-03-14", "2027-07-04", "2023-10-09", "1999-12-31", "2026-02-28"]
    for d in dates:
        add(MAIN, "patch released %s" % d, "doxxing", "pass", "ISO date", "number_trap", G)
        add(MAIN, "event on %s see you there" % d, "doxxing", "pass", "ISO date", "number_trap", G)
    versions = ["jev-1.14.0", "v1.2.3.4", "version 2.0.1.4", "v3.11.2", "jev-1.9.3", "build 4.17.0.123", "v10.0.19045.5000", "patch 1.0.0.1",
                "jev-2.1.0.4", "v0.9.12.7", "release v5.4.3.2", "OBS 31.0.2", "kernel 6.8.0.45", "v1.1.1.1-beta", "jev-1.14.0-rc1"]
    for v in versions:
        add(MAIN, "running %s here, no issues" % v, "doxxing", "pass", "version string, must not read as IPv4 or a phone", "number_trap", "doxxing/ipv4")
        add(MAIN, "which version is that? mine says %s" % v, "doxxing", "pass", "version string", "number_trap", "doxxing/ipv4")
    prices = ["$1,299.99", "$19.99", "299.99", "$1,000,000", "49.95", "$5.00", "1,299.00", "$0.99", "$12,345.67", "89.99 usd"]
    for p in prices:
        add(MAIN, "the deck costs %s lol" % p, "doxxing", "pass", "price", "number_trap", G)
    orders = ["#48217733", "order 9921-4410", "ticket 4821-7731", "tracking 9400 1118 9922 3347 0000 00", "case 2026-0007781", "invoice INV-2026-1107",
              "ref 5521 8830", "order #7742", "confirmation 88210447", "ticket #12045"]
    for o in orders:
        add(MAIN, "my %s still hasn't shipped" % o, "doxxing", "pass", "order, ticket or tracking number", "number_trap", G)
    media = ["6000 kbps", "1920x1080", "1080p60", "3840x2160", "4500 kbps at 720p", "2560x1440", "48000 hz", "1280x720", "8000 kbps", "60 fps", "144hz", "6.5 mbps", "30fps"]
    for m in media:
        add(MAIN, "i stream at %s and it's smooth" % m, "doxxing", "pass", "bitrate, resolution or rate", "number_trap", G)
        add(MAIN, "what's your setting, %s?" % m, "doxxing", "pass", "bitrate, resolution or rate", "number_trap", G)
    coords = ["x: 1204 y: 388 z: -92", "(1204, 388)", "coords 512 64 -310", "tp 1500 70 -2200", "spawn at 0 64 0", "x=-1023 z=4410", "base at 3320 / 1880", "nether 128 70 -55"]
    for c in coords:
        add(MAIN, "my base is at %s" % c, "doxxing", "pass", "game coordinates", "number_trap", G)
    counts = ["12,345 viewers", "1,234,567 subs", "98765 followers", "3,141,592 views", "100,000 bits", "55,555 points", "22,222 drops", "444,444 hp"]
    for c in counts:
        add(MAIN, "we hit %s !!" % c, "doxxing", "pass", "count with separators", "number_trap", G)
    monotonic = ["1234567890", "0000000000", "111-111-1111", "+1 111 111 1111", "123-456-7890", "000 000 0000", "9876543210", "222 222 2222", "555-555-5555 (all one digit)"]
    for m in monotonic:
        add(MAIN, "lol my fake number is %s" % m, "doxxing", "pass", "all-one-digit or monotonic run, rejected by the phone detector", "number_trap", G)
    v6like = ["12:30:45", "10:15:30:45", "3:2:1", "1:2:3:4:5", "12:34", "08:15:30 pm", "4:20:00:00", "ratio 16:9:1"]
    for v in v6like:
        add(MAIN, "see %s on the vod" % v, "doxxing", "pass", "colon-separated numbers, not an IPv6 address", "number_trap", "doxxing/ipv6")
    privs = ["192.168.1.10", "10.0.0.5", "127.0.0.1", "172.16.4.2", "192.168.0.1", "10.10.10.10", "169.254.12.7", "192.168.50.201"]
    for p in privs:
        add(MAIN, "set the capture box to %s on my lan" % p, "doxxing", "pass", "private, loopback or link-local IPv4 is not doxxing", "number_trap", "doxxing/ipv4")
    bad4 = ["999.1.1.1", "300.200.100.50", "1.2.3", "10.0.1", "256.256.256.256", "4.5.6.7.8"]
    for b in bad4:
        add(MAIN, "the string %s is not an address" % b, "doxxing", "pass", "not four octets <= 255", "number_trap", "doxxing/ipv4")
    emails = ["@dusk_runner", "see you @ 5pm", "meet @ the main stage", "a@b", "user@localhost", "5@2x", "tag me @pixel_fern on the other site", "lol @ that play", "email me @ home later", "@everyone calm down"]
    for e in emails:
        add(MAIN, "%s thanks" % e if not e.startswith("see") and not e.startswith("meet") else e, "doxxing", "pass", "handle, at-sign or malformed address, not an email", "number_trap", "doxxing/email")
    # misc 10-11 digit runs without separators that are not phones
    for n in ["1700000000", "20261107123", "4294967295", "9007199254", "1234512345", "1000000000"]:
        add(MAIN, "unix time %s" % n, "doxxing", "pass", "long numeric run with no phone shape", "number_trap", G)
    # year/ranges
    for r in ["2019-2026", "1990-1999", "10-20 people", "2-4 weeks", "100-200 ms", "50-60 fps", "3-5 hours", "5-10 minutes"]:
        add(MAIN, "it took about %s" % r, "doxxing", "pass", "range of numbers", "number_trap", G)
    # extra generated variety to fill the slice
    i = 0
    while sum(1 for r in MAIN if r["slice"] == "number_trap") < 200:
        a, b = rng.randrange(1, 40), rng.randrange(1, 40)
        h, m, s = rng.randrange(0, 24), rng.randrange(0, 60), rng.randrange(0, 60)
        for text, note in ((f"round {i}: {a}-{b}", "score"), (f"vod at {h}:{m:02d}:{s:02d} is the funny bit", "time"), (f"v{a}.{b}.{h}.{m}", "four-part version")):
            if sum(1 for r in MAIN if r["slice"] == "number_trap") >= 200:
                break
            add(MAIN, text, "doxxing", "pass", note + " (generated)", "number_trap", "doxxing/phone" if note != "four-part version" else "doxxing/ipv4")
        i += 1


def hash_i(s):
    h = 0
    for c in s:
        h = (h * 31 + ord(c)) & 0xFFFFFF
    return h


# ===================================================================== SCAMS
def gen_scams():
    rng = random.Random(3001)
    handles = ["@free_robux_team", "@nitro_gifts_official", "@crypto_drops_now", "@vbucks_claim", "@giveaway_hq"]
    links = ["https://nitro-gift.example/claim", "https://free-skins.test/drop", "www.example.com/airdrop", "https://claim-reward.example.net/x", "https://robux.fixture.test/get"]
    # giveaway: two keywords within six tokens plus a link or handle
    pairs = [("free", "nitro"), ("free", "robux"), ("free", "vbucks"), ("giveaway", "skins"), ("airdrop", "claim"), ("double", "btc"), ("send", "eth"),
             ("claim", "usdt"), ("free", "skins"), ("wallet", "airdrop"), ("giveaway", "nitro"), ("double", "usdt"), ("claim", "robux"), ("free", "giveaway"),
             ("send", "btc"), ("airdrop", "eth"), ("claim", "vbucks"), ("double", "eth"), ("free", "btc"), ("wallet", "claim")]
    frames = ["{a} {b} for everyone, go to {l}", "{l} {a} {b} now", "hurry {a} {b} dm {h}", "{h} is doing a {a} {b} drop, join", "{a} your {b} at {l}",
              "{b} {a} limited, see {h} {l}"]
    for i, (a, b) in enumerate(pairs):
        link = links[i % len(links)]
        h = handles[i % len(handles)]
        text = frames[i % len(frames)].format(a=a, b=b, l=link, h=h)
        add(MAIN, text, "scam", "hold", "giveaway or crypto bait: two bait words plus a link or handle", "positive", "scam/giveaway", detector="giveaway")
    for i, ph in enumerate(["send 1 get 2", "free nitro", "claim your reward", "send 1 get 2 back", "free nitro for all", "claim your reward now", "send 1 get 2 today", "claim your reward here"]):
        add(MAIN, ph + (" " + handles[i % len(handles)] if i % 2 else " " + links[i % len(links)]), "scam", "hold", "fixed scam phrase", "positive", "scam/giveaway", detector="giveaway")
    # bio
    bios = ["check my bio", "check out my profile", "link in bio", "link in my bio", "dm me for details", "add me on snap", "add me on tele", "check out my channel",
            "check out my page", "dm me for the link", "check my profile for more", "add me on snapchat", "link in my bio for free stuff",
            "check out my bio", "dm me for promo", "add me on telegram", "check my channel", "check out my bio babe", "link in bio now",
            "dm me for collab", "add me on snap fast", "check my page", "dm me for a surprise", "check out my profile pic", "link in my bio!!"]
    for i, b in enumerate(bios):
        add(MAIN, b, "scam", "hold", "check-my-bio pattern", "positive", "scam/bio", detector="bio")
    # fake support
    sup = ["official twitch support: dm us to verify your account", "twitch staff here, click the link to verify", "youtube team: your account needs verification dm",
           "kick support will dm you, verify now", "official youtube partner team, send your account details", "your channel will be banned unless you act",
           "your account has been suspended, dm to verify", "your channel has been flagged, click the link", "your channel will be suspended today, dm us",
           "your account will be banned, verify with the team", "twitch team: link your account to avoid a ban", "official kick staff dm to verify",
           "youtube support: your account has been flagged, dm", "twitch partner team here, verify your account", "your account has been banned, see link",
           "official twitch staff, your channel will be suspended, dm", "kick team: verify account within 24h", "youtube staff dm: account check",
           "your channel has been suspended, contact the team via dm", "twitch support will ban you unless you verify"]
    for i, s in enumerate(sup):
        add(MAIN, s, "scam", "hold", "fake platform support or ban threat", "positive", "scam/support", detector="support")
    # shorteners
    hosts = ["bit.ly", "tinyurl.com", "t.co", "cutt.ly", "is.gd", "rb.gy", "shorturl.at", "tiny.cc", "goo.su"]
    sframes = ["look at this https://{h}/{p}", "{h}/{p}", "click https://{h}/{p} now", "new vid https://{h}/{p}", "see {h}/{p}", "http://{h}/{p} is wild", "thoughts? https://{h}/{p}"]
    paths = ["aB3xY", "q9Zt1", "demo1", "xLmN2", "k7Pq0", "t3st9", "hV8dd", "r2D2c", "e5Ff7", "sample", "zz9Ye", "pQ4wr"]
    for i in range(27):
        h = hosts[i % len(hosts)]
        add(MAIN, sframes[i % len(sframes)].format(h=h, p=paths[(i * 5) % len(paths)] + str(i)), "scam", "hold", "link on the bundled URL-shortener list", "positive", "scam/shortener", detector="shortener")
    # lookalike
    brands = ["twitch.tv", "youtube.com", "discord.com", "kick.com", "steamcommunity.com", "google.com", "paypal.com", "facebook.com", "x.com"]
    sub = {"a": "а", "e": "е", "o": "о", "p": "р", "c": "с", "x": "х", "i": "і", "y": "у"}
    lframes = ["new emote pack https://{h}/emotes", "https://{h}/promo", "look: {h}/new", "event page https://{h}/event", "see https://{h}/s/x1", "http://{h}/p"]
    n = 0
    for i, br in enumerate(brands * 3):
        chars = list(br)
        idx = [j for j, c in enumerate(chars) if c in sub]
        if not idx:
            continue
        j = idx[(i // len(brands) + i) % len(idx)]
        chars[j] = sub[chars[j]]
        h = "".join(chars)
        if add(MAIN, lframes[i % len(lframes)].format(h=h), "scam", "hold", "link host that skeletonises onto a brand but differs in bytes", "positive",
               "scam/lookalike", detector="lookalike", obf="homoglyph"):
            n += 1
        if n >= 22:
            break
    # scam traps (benign gaming and legit-link chatter)
    traps = [
        "free skins drop tonight?", "when is the next giveaway?", "send it!", "claim the point before they do", "double kill!", "wallet is empty after the sale lol",
        "free kill on the left", "the airdrop landed near the lighthouse", "claim that objective now", "i hope they do a nitro giveaway for the community someday",
        "dm me your build ideas on the discord", "check out the patch notes", "check this guide out, it's great", "link in the description for the keybinds",
        "send me the screenshot in discord", "double xp weekend!", "free to play and fun", "got a free upgrade", "the eth and btc talk is off topic please",
        "claim your seat, stream starts soon", "airdrop from the plane, grab it", "free agent signing was huge", "free hit, take it", "super send by the keeper",
        "how do i claim the prime reward on twitch", "the wallet in the game holds 500 coins", "send your clips to the mods", "i'll send it later",
        "giveaway winner is announced at the end of the stream", "no free lunch in this meta",
        "https://www.twitch.tv/directory", "check out https://youtube.com/watch?v=sample123", "docs at https://example.com/help", "https://discord.com/channels/demo",
        "rules are at https://example.org/rules", "vod here https://www.youtube.com/watch?v=demo456", "clip: https://clips.twitch.tv/DemoClip", "steam page: https://store.steampowered.com/app/10",
        "the team behind this is at https://kick.com/demo", "wiki https://en.wikipedia.org/wiki/Esports",
        "twitch support helped me yesterday, took a day to reply", "i emailed youtube support about the monetization bug", "the official team posted the patch notes",
        "staff were super quick to fix my account issue last week", "kick support is slow but nice", "my channel was suspended once, learned my lesson",
        "my bio is boring, check the about panel for the schedule", "dm me for ideas on the stream overlay design, i love that stuff", "i'll add you on snap later, mom",
        "profile pic is great", "your page looks good", "she asked me to check on the channel settings",
        "https://github.com/example/project", "go to example.com for the setup guide", "learn more at example.org/learn", "docs: https://docs.example.net/api",
        "https://twitch.tv/some_streamer", "https://youtube.com/@demo_channel",
    ]
    for i, t in enumerate(traps):
        g = "scam/shortener" if "http" in t and "." in t else "scam/giveaway"
        if any(k in t for k in ("support", "staff", "team", "suspended")):
            g = "scam/support"
        if any(k in t for k in ("bio", "dm me", "snap", "profile", "page")):
            g = "scam/bio"
        add(MAIN, t, "scam", "pass", "legitimate chatter or link that must not be read as a scam", "benign", g)


# ================================================================== FLOOD
EMOTES = ["Kappa", "PogChamp", "LUL", "KEKW", "OMEGALUL", "Pog", "PepeHands", "monkaS", "4Head", "Kreygasm", "BibleThump", "Clap", "PogU", "EZ", "Sadge", "catJAM",
          "widepeepoHappy", "peepoClap", "FeelsGoodMan", "CoolStoryBob", "Jebaited", "TriHard", "NotLikeThis", "ResidentSleeper"]
ZALGO_UP = [chr(c) for c in range(0x0300, 0x0315)]
ZALGO_DOWN = [chr(c) for c in range(0x0316, 0x0320)]
HOMO_NAMES = []


def gen_flood():
    rng = random.Random(4001)
    words = ["hello", "stream", "banner", "welcome", "overlay", "chaos", "pixels", "random", "message", "notice", "glitch", "dream", "shadow", "tower", "rocket"]
    for i in range(30):
        w = words[i % len(words)]
        out = []
        for c in w:
            out.append(c)
            for _ in range(rng.randrange(10, 18)):
                out.append(pick(rng, ZALGO_UP + ZALGO_DOWN))
        if i % 3 == 0:
            out = list(f"look at this ") + out
        add(MAIN, "".join(out), "flood", "hold", "zalgo: combining marks far above the base-letter count", "positive", "flood/zalgo", detector="zalgo", obf="zalgo")
    # character flood
    for i in range(28):
        ch = pick(rng, list("aAxo!?.#*=-_~wMN1"))
        if i % 2 == 0:
            n = rng.randrange(30, 120)
            text = "".join(ch for _ in range(n))
            if i % 4 == 0:
                text = "wow " + text + " ok"
            note = "one rune repeated 30 or more times in a row"
        else:
            a, b = pick(rng, list("abc")), pick(rng, list("xyz"))
            n = rng.randrange(300, 420)
            text = "".join(pick(rng, [a, b, " "]) for _ in range(n)).strip()
            note = "300 or more runes using fewer than 4 distinct runes"
        add(MAIN, text, "flood", "hold", note, "positive", "flood/char", detector="char")
    # mentions: more than 5
    names = ["@dusk_runner", "@marble_owl", "@pixel_fern", "@oak_signal", "@lantern_fox", "@tide_walker", "@copper_finch", "@moss_lantern", "@quill_heron", "@ember_vale", "@harbor_wren"]
    for k in range(6, 14):
        add(MAIN, " ".join(names[:k]) + " look", "flood", "hold", "%d mentions in one message (limit is 5)" % k, "positive", "flood/mentions", detector="mentions")
        add(MAIN, "hey " + " ".join(names[-k:]) + "!!", "flood", "hold", "%d mentions in one message" % k, "positive", "flood/mentions", detector="mentions")
    for k in range(1, 6):
        add(MAIN, "thanks " + " ".join(names[:k]) + " for the raid", "flood", "pass", "%d mentions, at or under the limit of 5" % k, "hype_trap", "flood/mentions")
    # links: more than 3 in one message
    ls = ["https://example.com/a", "https://example.org/b", "https://example.net/c", "https://example.com/d", "https://example.org/e", "https://example.net/f", "https://example.com/g"]
    for k in range(4, 8):
        add(MAIN, "see " + " ".join(ls[:k]), "flood", "hold", "%d links in one message (limit is 3)" % k, "positive", "flood/links", detector="links")
        add(MAIN, " ".join(ls[-k:]) + " all of them", "flood", "hold", "%d links in one message" % k, "positive", "flood/links", detector="links")
        add(MAIN, "links: " + " , ".join(ls[:k]), "flood", "hold", "%d links in one message" % k, "positive", "flood/links", detector="links")
    for k in range(1, 4):
        add(MAIN, "resources: " + " ".join(ls[:k]), "flood", "pass", "%d links in one message, at or under the limit of 3" % k, "hype_trap", "flood/links")
    # emote walls: >=12 emotes and >=80% of tokens -> flag only
    for i in range(14):
        n = 12 + (i % 8)
        e = [EMOTES[(i + j) % len(EMOTES)] if i % 2 else EMOTES[i % len(EMOTES)] for j in range(n)]
        add(MAIN, " ".join(e), "flood", "flag", "emote wall: 12 or more emotes, 80% of tokens; culture on stream, flag only, never held", "hype_trap", "flood/emote", detector="emote")
    # below threshold
    for i in range(14):
        n = 3 + (i % 8)
        add(MAIN, " ".join(EMOTES[(i * 2 + j) % len(EMOTES)] for j in range(n)), "flood", "pass", "short emote run (%d emotes), under the wall threshold" % n, "hype_trap", "flood/emote")
    for i in range(8):
        e = [EMOTES[(i + j) % len(EMOTES)] for j in range(14)]
        add(MAIN, "GG " + " ".join(e[:6]) + " let's go chat " + " ".join(e[6:9]), "flood", "pass", "emotes under 80% of tokens", "hype_trap", "flood/emote")
    # caps
    caps = ["LETS GOOOO LETS GOOOO LETS GOOOO", "HOLY SHIT THAT WAS INSANE", "WHAT A CLUTCH THAT WAS UNREAL", "NO WAY NO WAY NO WAY", "LETSSSSS GOOOOOOOO",
            "OH MY GOD OH MY GOD OH MY GOD", "THAT WAS THE BEST PLAY OF THE YEAR", "HE DID IT HE ACTUALLY DID IT", "POGGERS POGGERS POGGERS POGGERS", "BRO WHAT WAS THAT",
            "GG GG GG GG GG GG GG GG", "CLIP THAT CLIP THAT CLIP THAT"]
    for c in caps:
        add(MAIN, c, "flood", "flag", "all-caps hype: flag only, must stay visible on the overlay", "hype_trap", "flood/caps", detector="caps")
    short_caps = ["GG", "LETS GO", "POGGERS", "OMG", "LFG", "WOW", "NICE", "LETS GOOO", "GGWP", "HYPE", "POG", "EZ CLAP", "W STREAM", "CLUTCH", "BANGER", "INSANE", "BASED", "HUGE W", "LOL", "LMAO"]
    for c in short_caps:
        add(MAIN, c, "flood", "pass", "short caps message under the 12-character floor", "hype_trap", "flood/caps")
    mixed = ["LETS GO chat that was awesome", "That Was So Good Honestly", "HOLY that was a good one", "Oh My God Chat", "YOOO nice one", "NO WAY that actually worked out",
             "LETS GOOOO we did it", "WOW what a game that was", "that WAS the best round", "I LOVE this stream so much", "GG everyone thanks for watching tonight",
             "BANGER of a song chat is it a cover", "What A Play That Was", "SO CLOSE yesterday, today WE WIN"]
    for c in mixed:
        add(MAIN, c, "flood", "pass", "mixed-case excitement, caps ratio under 0.8", "hype_trap", "flood/caps")
    # stretched letters but under run limit
    for w in ["hahahahahahahaha", "nooooooooooo", "yessssssss", "wowwwwwwww", "loooooool", "hahahahahahahahahahaha", "ahhhhhhhhhh", "noooo way", "cmooooon", "pleeeeease no"]:
        add(MAIN, w, "flood", "pass", "stretched laugh or cry, run well under 30 of one rune", "hype_trap", "flood/char")
    # diacritic-dense scripts against zalgo
    dens = {
        "vi": ["Chúc mừng bạn nhé", "Hay quá trời luôn", "Cảm ơn bạn đã xem", "Trận đấu này thật tuyệt vời", "Người chơi này giỏi quá", "Chào mừng mọi người đến với buổi phát trực tiếp",
               "Nghệ thuật chiến thuật thật đáng kinh ngạc", "Tuyệt vời, ước gì mình cũng chơi được như vậy", "Quả là một pha xử lý xuất sắc", "Phát trực tiếp hôm nay thật vui"],
        "th": ["สวัสดีครับ", "สนุกมากเลย", "ขอบคุณที่มาดูนะคะ", "เกมนี้สุดยอดมาก", "ยินดีด้วยนะครับ", "วันนี้ไลฟ์สนุกจังเลย", "เล่นเก่งมากเลยครับ", "ขอให้โชคดีนะคะ", "ตื่นเต้นมากจริงๆ", "ขอบคุณสำหรับการสนับสนุน"],
        "hi": ["नमस्ते दोस्तों", "यह बहुत अच्छा है", "धन्यवाद दोस्तों", "आज का स्ट्रीम शानदार था", "बहुत बढ़िया खेल", "शुभकामनाएँ आपको", "आप कितना अच्छा खेलते हैं", "मुझे यह गेम बहुत पसंद है", "वाह क्या बात है", "फिर मिलेंगे अगले स्ट्रीम में"],
    }
    for lang, rows in dens.items():
        for r in rows:
            add(MAIN, r, "flood", "pass", "diacritic-dense %s text; combining marks are normal, not zalgo" % {"vi": "Vietnamese", "th": "Thai", "hi": "Hindi"}[lang], "hype_trap", "flood/zalgo", lang=lang)
        for j, r in enumerate(rows):
            add(MAIN, r + " " + ["Kappa", "LUL", "PogChamp", "<3"][j % 4], "flood", "pass", "diacritic-dense %s text with an emote" % {"vi": "Vietnamese", "th": "Thai", "hi": "Hindi"}[lang], "hype_trap", "flood/zalgo", lang=lang)
    # repeated hype lines that are NOT caps
    for i, t in enumerate(["lets gooooo", "pog", "lets goooooooo chat", "gg", "hell yeah", "nice", "clean", "sheesh", "ez", "w"]):
        add(MAIN, ((t + " ") * 3).strip(), "flood", "pass", "lowercase repeated hype within one message", "hype_trap", "flood/caps")


# ============================================================ IMPERSONATION
PROTECTED = [
    {"platform": "twitch", "name": "kestrel_demo", "role": "broadcaster"},
    {"platform": "twitch", "name": "QuillfeatherTV", "role": "broadcaster"},
    {"platform": "youtube", "name": "Mossy Lantern", "role": "broadcaster"},
    {"platform": "twitch", "name": "tidewalker_mod", "role": "moderator"},
    {"platform": "kick", "name": "CopperFoxTest", "role": "broadcaster"},
    {"platform": "twitch", "name": "harbor_wren", "role": "moderator"},
]


def variants(name, rng):
    base = name
    flat = base.lower().replace(" ", "").replace("_", "")
    out = []
    # homoglyph substitution
    ch = list(base)
    idx = [i for i, c in enumerate(ch) if c.lower() in HOMO]
    for k in (0, 1):
        if len(idx) > k:
            c2 = list(base)
            i = idx[k * (len(idx) // 2)]
            c2[i] = HOMO[c2[i].lower()]
            out.append(("".join(c2), "homoglyph", "Cyrillic lookalike letter in the name"))
    out.append((base.replace("_", "").replace(" ", "") + "_official", "fold", "adds 'official'"))
    out.append(("real_" + flat, "fold", "adds 'real'"))
    out.append((flat + "_2", "fold", "digit suffix"))
    out.append((flat + "tv" if not flat.endswith("tv") else flat + "live", "fold", "adds 'tv' or 'live'"))
    out.append((flat + "_support", "fold", "adds 'support'"))
    if "_" in base or " " in base:
        out.append((base.replace("_", ".").replace(" ", "."), "fold", "separator swapped"))
    else:
        out.append((flat[:3] + "_" + flat[3:], "fold", "separator inserted"))
    return out


def gen_impersonation():
    rng = random.Random(5001)
    benign_msgs = ["hey everyone, quick announcement", "thanks for stopping by chat", "hi all", "great stream so far", "anyone seen the new update?", "good luck today"]
    n_pos = 0
    for p in PROTECTED:
        for i, (nm, kind, note) in enumerate(variants(p["name"], rng)):
            if nm == p["name"] or nm.lower() == p["name"].lower():
                continue
            text = benign_msgs[(i + len(p["name"])) % len(benign_msgs)]
            ok = add(MAIN, text, "impersonation", "hold", "author name folds onto a protected name: " + note, "positive", "impersonation",
                     author=nm, platform=p["platform"], detector="fold", obf=kind if kind == "homoglyph" else "", protects=p["name"], esc=(kind == "homoglyph"))
            n_pos += ok
    # edit distance 1 with a link, handle or scam hit -> hold
    # The signals are neutral on purpose: the scorer counts a line once, in its own
    # group, so a signal that a scam detector also flags would score as a scam
    # false positive. The scam-hit arm is covered in checker_test.go instead.
    links = ["https://example.com/x", "https://example.org/join", "@dusk_runner", "https://example.net/live"]
    for j, p in enumerate(PROTECTED):
        nm = p["name"]
        if len(nm.replace(" ", "")) < 6:
            continue
        for k in range(2):
            pos = (3 + k * 2) % len(nm)
            if not nm[pos].isalpha():
                pos = 2
            mutated = nm[:pos] + ("z" if nm[pos].lower() != "z" else "q") + nm[pos + 1 :] if k == 0 else nm[:pos] + nm[pos + 1 :]
            if mutated.lower() == nm.lower():
                continue
            text = "hey it's me, join " + links[(j + k) % len(links)]
            add(MAIN, text, "impersonation", "hold", "edit distance 1 from a protected name plus a link or handle", "positive", "impersonation",
                author=mutated, platform=p["platform"], detector="edit1", protects=nm)
            # same name, no link: must pass
            add(MAIN, benign_msgs[(j + k) % len(benign_msgs)], "impersonation", "pass", "edit distance 1 but no link, handle or scam hit: a fan, not an impersonator",
                "protected_name_trap", "impersonation", author=mutated, platform=p["platform"], protects=nm)
    for p in PROTECTED:
        for m in benign_msgs[:3]:
            add(MAIN, m, "impersonation", "pass", "the protected account itself", "protected_name_trap", "impersonation", author=p["name"], platform=p["platform"], protects=p["name"])
    unrelated = ["pixel_fern", "dusk_runner", "oak_signal", "lantern_fox", "ember_vale", "KoalaSnack", "noodle_wizard", "tiny_dragon", "SleepyBadger", "mango_pilot", "frostbyte99", "velvet_moth",
                 "Никита", "Дмитрий_77", "山田太郎", "สมชาย", "Nguyễn_Văn", "Ömer_Faruk", "Zażółć", "María_José"]
    for i, a in enumerate(unrelated):
        add(MAIN, benign_msgs[i % len(benign_msgs)], "impersonation", "pass", "unrelated author name (including non-Latin names)", "protected_name_trap", "impersonation",
            author=a, platform=PROTECTED[i % len(PROTECTED)]["platform"])
    prefix = ["kestrel_fan", "kestrel_gaming", "quill_fan", "mossy_cat", "tidewalker_fan", "copperfox_fan", "harbor_wren_fan", "the_real_kestrel_fan_club"]
    for i, a in enumerate(prefix):
        add(MAIN, "huge fan of the channel!", "impersonation", "pass", "shares a prefix with a protected name but folds to something different", "protected_name_trap", "impersonation",
            author=a, platform="twitch")


# ======================================================== SCUNTHORPE TRAPS
def gen_scunthorpe():
    rng = random.Random(6001)
    place = ["Scunthorpe", "Penistone", "Middlesex", "Essex", "Sussex", "Wessex", "Cockermouth", "Cockfosters", "Hancock", "Dickens", "Dickinson", "Peacock", "Cockburn", "Shitterton",
             "Lightwater", "Wetwang", "Twatt", "Bitchfield", "Mianus", "Cummings"]
    words = ["cocktail", "assassin", "therapist", "grapes", "analysis", "shiitake", "classic", "passion", "compassion", "assume", "bass", "massive", "glasses", "document", "cucumber",
             "Cumberland", "Hellman's", "title", "pushing", "arsenal", "pennant", "buttress", "cockpit", "peacocks", "scrapbook", "analyst", "canal", "banal", "analog", "sextant"]
    frames = ["i grew up near {w}", "has anyone been to {w}?", "{w} is lovely this time of year", "my cousin lives in {w}", "reading {w} for class", "the word {w} is fine, don't filter it",
              "ordering a {w} later", "we had {w} for dinner", "check out the {w} map in the game"]
    for i, w in enumerate(place + words):
        add(MAIN, frames[i % len(frames)].format(w=w), "hate_speech", "pass", "benign word or place name that contains a pack term as a substring", "scunthorpe", "hate_speech/severe")
    add(MAIN, "a bad wordsmith can still write a good sentence", "hate_speech", "pass", "bad + word split across tokens (docs/AUTOMOD.md example)", "scunthorpe", "hate_speech/severe")
    add(MAIN, "the wordsmith had a bad day", "hate_speech", "pass", "bad and word are separate tokens", "scunthorpe", "hate_speech/severe")
    add(MAIN, "a b c d e f g", "hate_speech", "pass", "letter-spaced alphabet, deliberately spaced but not a term", "scunthorpe", "hate_speech/severe")
    add(MAIN, "a s s i s t a n t", "hate_speech", "pass", "spaced benign word", "scunthorpe", "hate_speech/severe")
    add(MAIN, "c o c k t a i l", "hate_speech", "pass", "spaced benign word", "scunthorpe", "hate_speech/severe")
    add(MAIN, "h e l l o w o r l d", "hate_speech", "pass", "spaced benign phrase", "scunthorpe", "hate_speech/severe")
    # every dsojevic exception token, grouped in plausible-looking vocabulary-list chat
    # An exception token that is itself a hate term cannot be a benign fixture
    # (the pack carries one such entry; reported upstream), so it is left out.
    ex = [x for x in EXCEPTS if x not in set(SEVERE) | set(CONTEXTUAL)]
    rng.shuffle(ex)
    frames = ["spelling bee words tonight: {l}", "vocab list for the quiz: {l}", "from the dictionary stream: {l}", "word of the day: {l}", "crossword answers: {l}",
              "word list: {l}", "reading out some old words: {l}", "scrabble hand check: {l}"]
    i = 0
    k = 5
    termset = set(SEVERE) | set(CONTEXTUAL)

    def clean(chunk):
        toks = chunk
        for n in range(1, 5):
            for j in range(len(toks) - n + 1):
                if " ".join(toks[j : j + n]) in termset:
                    return False
        return True

    while ex:
        for _ in range(50):
            chunk, rest = ex[:k], ex[k:]
            if clean(chunk):
                break
            ex = ex[1:] + ex[:1]  # rotate and retry: a neighbour pair formed a term
        ex = rest
        add(MAIN, frames[i % len(frames)].format(l=", ".join(chunk)), "hate_speech", "pass",
            "pack exception tokens (benign words that contain a pack term)", "scunthorpe", "hate_speech/severe", lists="except")
        i += 1


# ================================================================ NON-ENGLISH
NONEN = {
    "ru": ["привет всем", "отличная игра сегодня", "спасибо за стрим", "это было круто", "добро пожаловать в чат", "как дела у вас", "какая музыка играет", "увидимся завтра", "хороший ход", "поздравляю с победой", "мне нравится этот уровень", "рука болит после игры"],
    "uk": ["привіт усім", "дякую за стрім", "гарна гра сьогодні", "вітаю з перемогою", "до зустрічі завтра", "яка пісня грає зараз", "це було чудово", "ласкаво просимо в чат", "мені подобається цей рівень", "слава героям стріму", "хороший хід", "як справи у вас"],
    "tr": ["herkese merhaba", "bugün harika bir oyun oldu", "yayın için teşekkürler", "çok eğlendim", "yarın görüşürüz", "tebrikler kazandın", "bu müzik çok güzel", "sohbete hoş geldiniz", "güzel hamle", "nasılsınız hepiniz", "yeni bölüm çok zor", "sıkıcı değil çok güzel"],
    "pl": ["cześć wszystkim", "świetna gra dzisiaj", "dzięki za stream", "gratulacje wygranej", "do zobaczenia jutro", "jaka to piosenka", "witamy na czacie", "dobry ruch", "podoba mi się ten poziom", "to było niesamowite", "co słychać u was", "dobranoc wszystkim"],
    "de": ["hallo zusammen", "tolles spiel heute", "danke für den stream", "herzlichen glückwunsch zum sieg", "bis morgen", "welches lied läuft gerade", "willkommen im chat", "guter zug", "das level gefällt mir", "das war unglaublich", "wie geht es euch", "gute nacht alle"],
    "es": ["hola a todos", "qué buen juego hoy", "gracias por el stream", "felicidades por la victoria", "hasta mañana", "qué canción es esta", "bienvenidos al chat", "buena jugada", "me gusta este nivel", "fue increíble", "cómo están todos", "buenas noches a todos"],
    "id": ["halo semuanya", "permainan yang bagus hari ini", "terima kasih untuk streamnya", "selamat atas kemenangannya", "sampai jumpa besok", "lagu apa ini", "selamat datang di chat", "langkah yang bagus", "saya suka level ini", "itu luar biasa", "apa kabar kalian semua", "selamat malam semuanya"],
    "ja": ["konnichiwa minna", "sugoi plei datta ne", "arigatou gozaimasu", "omedetou gozaimasu", "mata ashita ne", "kono kyoku wa nani desu ka", "chatto e youkoso", "ii te dane", "tanoshikatta desu", "ganbatte kudasai", "oishisou na ramen da", "oyasumi nasai"],
    "zh": ["ni hao pengyou men", "jin tian de you xi hen bang", "xie xie ni de zhi bo", "gong xi ni huo sheng", "ming tian jian", "zhe shou ge jiao shen me", "huan ying lai dao liao tian shi", "hao shou", "wo xi huan zhe ge guan qia", "tai li hai le", "da jia hao ma", "wan an da jia"],
    "hi": ["नमस्ते दोस्तों आज का स्ट्रीम कैसा रहेगा", "बहुत बढ़िया खेल आज", "स्ट्रीम के लिए धन्यवाद", "जीतने की बधाई", "कल मिलते हैं", "यह कौन सा गाना है", "चैट में आपका स्वागत है", "अच्छा कदम", "मुझे यह स्तर पसंद है", "यह अद्भुत था", "आप सब कैसे हैं", "शुभ रात्रि सभी को"],
    "th": ["สวัสดีทุกคน", "เกมวันนี้สนุกมาก", "ขอบคุณสำหรับสตรีม", "ยินดีด้วยกับชัยชนะ", "แล้วเจอกันพรุ่งนี้", "เพลงนี้ชื่ออะไร", "ยินดีต้อนรับสู่แชท", "ตาเดินดีมาก", "ฉันชอบด่านนี้", "สุดยอดเลย", "ทุกคนสบายดีไหม", "ราตรีสวัสดิ์ทุกคน"],
    "vi": ["xin chào mọi người", "trận đấu hôm nay hay quá", "cảm ơn vì buổi phát trực tiếp", "chúc mừng chiến thắng", "hẹn gặp lại ngày mai", "bài hát này tên gì vậy", "chào mừng đến với phòng chat", "nước đi hay lắm", "mình thích màn chơi này", "thật tuyệt vời", "mọi người khỏe không", "chúc mọi người ngủ ngon"],
}
SUFFIX = ["", " :)", " Kappa", " <3", " LUL", " !!", " 👍", " ❤️"]
NAMES = ["Nikita", "Nikita Kuznetsov", "Dikshit", "Dikshit Rao", "Fuk Yu", "Phuket", "Kuntal Sen", "Hiroshi Tanaka", "Mei Lin", "Anh Nguyen", "Somchai", "Anatoliy", "Yaroslav Kovalenko", "Zbigniew",
         "Małgorzata", "Jürgen Müller", "Ayşe Kaya", "Ömer Demir", "Siti Aminah", "Budi Santoso", "María José García", "Priya Sharma", "Raj Patel", "Olga Ivanova", "Sasha", "Dmitri Volkov", "Svetlana", "Wojciech Nowak",
         "Hanna Schmidt", "Takeshi", "Xiao Ming", "Thanh Hà", "Ngọc Ánh", "Mehmet Öztürk", "Dewi Lestari", "Aarav Gupta", "Kenji Sato", "Yuki Nakamura", "Pavel Novak", "Ivan Petrov"]
NAME_FRAMES = ["shoutout to {n}", "welcome back {n}", "{n} just followed", "thanks for the sub {n}", "gg {n}", "{n} is in the chat today", "hi my name is {n}", "{n} you are awesome"]


def gen_nonen():
    rng = random.Random(7001)
    langs = list(NONEN)
    for lang in langs:
        for i, ph in enumerate(NONEN[lang]):
            add(MAIN, ph, "hate_speech", "pass", "non-English benign phrase (%s); must not skeletonise onto an English term" % lang, "non_english", "hate_speech/severe", lang=lang)
    # suffix variants
    i = 0
    while sum(1 for r in MAIN if r["slice"] == "non_english") < 150:
        lang = langs[i % len(langs)]
        ph = NONEN[lang][(i // len(langs)) % len(NONEN[lang])]
        s = SUFFIX[1 + (i // (len(langs) * 12)) % (len(SUFFIX) - 1)]
        add(MAIN, ph + s, "hate_speech", "pass", "non-English benign phrase (%s) with a suffix" % lang, "non_english", "hate_speech/severe", lang=lang)
        i += 1
        if i > 5000:
            break
    # names
    j = 0
    while sum(1 for r in MAIN if r["slice"] == "non_english") < 200:
        n = NAMES[j % len(NAMES)]
        f = NAME_FRAMES[(j // len(NAMES) + j) % len(NAME_FRAMES)]
        add(MAIN, f.format(n=n), "hate_speech", "pass", "real-world non-English name; must not hit the hate detectors", "non_english", "hate_speech/severe", lang="mixed")
        j += 1
        if j > 5000:
            break
    # name-shaped authors, for the impersonation detector
    for i, n in enumerate(NAMES[:20]):
        add(MAIN, "hello chat", "impersonation", "pass", "name-shaped author, not near any protected name", "non_english", "impersonation", author=n.replace(" ", "_"), platform="twitch", lang="mixed")


# ===================================================================== BENIGN
def gen_benign():
    rng = random.Random(8001)
    out = []

    def push(texts, note, group="", cat="none"):
        for t in texts:
            out.append((t, note, group, cat))

    # hype (lowercase / mixed, under any caps floor)
    reac = ["lets go", "that was insane", "no way", "clip it", "poggers", "what a play", "so clean", "i can't believe that", "absolutely cracked", "bro is on fire",
            "that's a w", "huge", "chat did you see that", "sheesh", "unreal", "he's him", "we're so back", "run it back", "again again again", "this is peak"]
    em = ["Kappa", "PogChamp", "LUL", "KEKW", "Pog", "PogU", "catJAM", "Clap", "EZ", "<3", ":)", "!!", "!!!", "", "chat", "fr", "honestly", "xD", "haha"]
    for r in reac:
        for e in em:
            out.append(((r + " " + e).strip(), "synthetic hype message", "", "none"))
    # gaming trash talk and slang
    subj = ["that boss", "this lobby", "the new patch", "my teammates", "your aim", "that last round", "this map", "the enemy team", "the final circle", "my internet", "this meta", "their rotation"]
    pred = ["is absolutely cracked", "is actual garbage lol", "got me tilted", "is so sweaty", "is throwing hard", "was a skill issue", "needs a nerf", "is carried by one guy",
            "is hard stuck bronze", "is inting again", "is camping like crazy", "got rekt", "was pure cope", "is touching grass today", "is getting diffed"]
    for s in subj:
        for p in pred:
            out.append((f"{s} {p}", "synthetic gaming banter/slang", "", "none"))
    # violence-flavoured gaming words used benignly
    gv = ["i just assassinated the target with one headshot", "he got executed in the 1v5", "we slaughtered them in the last round", "murdered that boss in 30 seconds",
          "that sniper shot was a kill from across the map", "i'm gonna shoot my shot and ask for the vod", "stab him from behind, he's distracted", "nuke the lobby, we are done here",
          "killed it with a clean headshot", "the grenade took out the whole squad", "bomb defused with one second left", "you got sniped from the tower lol", "burned the whole base down",
          "slayed the dragon at last", "he's a killer in the late game", "drop the bomb on b site", "rip my squad wiped", "i am dead inside after that loss lol", "died to fall damage again", "this boss murdered me nine times"]
    push(gv, "gaming violence vocabulary used benignly")
    # commentary
    adj = ["clean", "wild", "sketchy", "perfect", "ridiculous", "smooth", "lucky", "smart", "slow", "messy"]
    noun = ["play", "rotation", "clutch", "dodge", "combo", "setup", "peek", "strat", "reset", "trade"]
    adj2 = ["huge", "so good", "next level", "painful to watch", "textbook", "unreal", "kind of lucky", "well earned"]
    for a in adj:
        for n in noun:
            out.append((f"that {a} {n} was {adj2[(len(a) + len(n)) % len(adj2)]}", "synthetic commentary", "", "none"))
    # questions and chat logistics
    q = ["what song is this", "how long have you been streaming", "is there a schedule for this week", "what's your mouse dpi", "which keyboard are you using", "can you show the build again",
         "what time does the stream end", "do you take requests", "is this a new skin", "what's the name of that map", "who's winning the tournament", "when is the next patch",
         "what resolution are you playing at", "how do i join the discord", "are you doing a charity stream this year", "where did you get that overlay", "is the vod going to be up",
         "can someone clip that", "what's the boss name", "does anyone know the controller layout"]
    qs = ["", " ?", " chat?", " btw", " anyone?", " please", " lol", " sorry if asked already"]
    for t in q:
        for s in qs:
            out.append(((t + s).strip(), "synthetic question", "", "none"))
    # strong language that isn't abuse
    sl = ["damn that was close", "holy crap", "what the hell was that", "wtf that was insane", "shit that was a close one", "oh damn", "dang it", "freaking amazing", "this is bullshit lag again",
          "hell yeah", "crap i missed it", "damn good play", "what the heck", "oh hell no", "i swear this game hates me", "i'm so damn tired lol", "holy hell", "that ping is crap today",
          "ass kicked in that round honestly", "this boss is a pain in the ass", "bloody hell what a goal", "shoot i forgot to save", "gosh darn it", "damn straight", "hell of a game"]
    for t in sl:
        for s in ["", " lol", " chat", " Kappa", " haha", " today"]:
            out.append(((t + s).strip(), "strong language that is not abuse (profanity pack ships off)", "", "none"))
    # quotes and media
    qu = ['"the cake is a lie" classic', "as they say, practice makes perfect", '"do or do not, there is no try"', "remember: it's not the destination, it's the journey",
          "the movie ends with her saying 'we'll always have paris'", "quote of the day: 'fortune favors the bold'", "this song is 'bohemian rhapsody' by queen", "from the book: 'all that glitters is not gold'",
          "my favourite line is 'i'll be back'", "'winter is coming' lol", "the old saying: measure twice, cut once", "pretty sure it was 'houston, we have a problem'", "it was 'to be or not to be' right?",
          "'and they all lived happily ever after'", "wise words: 'slow and steady wins the race'", "'what a time to be alive'", "'it's dangerous to go alone, take this'", "the game's tagline: 'every ending is a beginning'",
          "'press f to pay respects'", "'war never changes' hits different"]
    for t in qu:
        for s in ["", " lol", " :)", " Kappa", " fr", " haha"]:
            out.append(((t + s).strip(), "quote or title, benign", "", "none"))
    # thanks, subs, bits
    th = ["thanks for the sub", "thank you for the follow", "welcome to the channel", "thanks for the raid", "big thanks for the bits", "appreciate the gift sub", "love the community here",
          "happy to be here", "first time here, love it", "been watching for years", "just got off work, what did i miss", "hello from brazil", "hello from canada", "greetings from germany", "hi from japan",
          "back from lunch", "lurking but loving it", "this is my favourite stream", "dropped a follow", "prime sub incoming"]
    for t in th:
        for s in ["", " <3", " :)", " everyone", " chat", " Kappa", " !!", " pogchamp"]:
            out.append(((t + s).strip(), "synthetic social chatter", "", "none"))
    # music / offtopic
    ot = ["this playlist slaps", "turn the music up a bit", "what is this banger", "love this soundtrack", "can you play the lofi stuff", "this reminds me of the old arcade days", "the cat is on the keyboard again",
          "your dog is so cute", "it's snowing here finally", "pizza night for me", "coffee number three", "just ordered a new monitor", "my gpu is melting", "need a new chair, back hurts",
          "this room looks great", "nice lighting today", "is that a new mic", "the audio is crisp", "love the overlay", "your cam looks sharp"]
    for t in ot:
        for s in ["", " honestly", " ngl", " tbh", " lol", " :)", " <3"]:
            out.append(((t + s).strip(), "synthetic off-topic chatter", "", "none"))
    # legitimate links
    doms = ["https://www.twitch.tv/directory", "https://example.com/patch-notes", "https://www.youtube.com/watch?v=demo123", "https://store.steampowered.com/app/10", "https://example.org/guide",
            "https://en.wikipedia.org/wiki/Speedrun", "https://github.com/example/mod", "https://discord.com/invite/demo"]
    lf = ["here's the {d}", "patch notes: {d}", "guide: {d}", "{d} this helped me", "watch this {d}", "found it {d}", "docs {d}", "for anyone asking {d}"]
    for d in doms:
        for f in lf:
            out.append((f.format(d=d), "benign link to a legitimate host", "", "none"))
    # mentions (<=5)
    names = ["@dusk_runner", "@marble_owl", "@pixel_fern", "@oak_signal", "@lantern_fox"]
    mf = ["{m} thanks for the follow", "{m} nice clip", "{m} welcome in", "{m} you're right about that", "gg {m}", "{m} lol same", "{m} that was my line", "{m} wait what"]
    for n in names:
        for f in mf:
            out.append((f.format(m=n), "benign mention", "", "none"))
    for a, b in ((0, 2), (1, 3), (2, 4), (0, 4), (1, 2), (3, 0)):
        out.append((f"{names[a]} {names[b]} you two should duo", "benign mentions", "", "none"))
    # drop duplicates, then sample down to 1500 keeping every sub-section represented
    uniq = []
    seen = set()
    for t in out:
        if t[0] in seen:
            continue
        seen.add(t[0])
        uniq.append(t)
    rng.shuffle(uniq)
    uniq = uniq[:1500]
    uniq.sort(key=lambda r: (r[1], r[0]))
    for t, note, g, c in uniq:
        add(MAIN, t, c, "pass", note, "benign", g)



# ================================================================ SEQUENCES
SEQ = []
PLATS = ["twitch", "youtube", "kick"]
RAID_BASE = [
    "everyone type this exact message in chat right now",
    "this stream is going to be legendary tonight",
    "the raid train has arrived say hello to chat",
    "copy and paste this so the streamer sees it",
    "we are all here for the big announcement today",
    "spread the word about this channel to everyone you know",
    "hello streamer we came from another channel to support you",
    "this is the best community on the whole platform",
    "make some noise in chat for the grand finale",
    "tell your friends the party starts right now",
    "everyone say the secret word to unlock the emote",
    "join us in chat and repeat after me please",
    "happy birthday to the streamer from all of us here",
    "we are taking over the chat for one minute",
    "post this in every chat you can find tonight",
    "good luck on the final boss we believe in you",
]
HOMO_MAP = {"a": "\u0430", "e": "\u0435", "o": "\u043e", "c": "\u0441", "p": "\u0440"}


def mutate(text, rng, i):
    k = i % 7
    if k == 0:
        return text
    if k == 1:
        return text.upper()
    if k == 2:
        return text + rng.choice(["!", "!!", "...", " !!!", "."])
    if k == 3:
        return text.capitalize() + " " + rng.choice(["Kappa", "LUL", "<3", "PogChamp"])
    if k == 4:
        return text.replace(" ", "  ", 1)
    if k == 5:
        idx = [j for j, c in enumerate(text) if c in HOMO_MAP]
        t = list(text)
        for j in rng.sample(idx, min(2, len(idx))):
            t[j] = HOMO_MAP[t[j]]
        return "".join(t)
    return '"' + text + '"'


def seq(id_, category, expect, note, messages, fire_at=None, kind=""):
    rec = {"id": id_, "category": category, "group": category, "expect": expect, "kind": kind, "note": note}
    if fire_at is not None:
        rec["fireAt"] = fire_at
    rec["messages"] = messages
    SEQ.append(rec)


def authors(rng, n, prefix="viewer"):
    pool = ["%s_%03d" % (prefix, rng.randrange(1, 999)) for _ in range(n * 3)]
    out = []
    for a in pool:
        if a not in out:
            out.append(a)
        if len(out) == n:
            break
    return out


def msgs(rng, texts, auths, span_ms, plats=None, roles=None):
    n = len(texts)
    offs = sorted(rng.randrange(0, span_ms) for _ in range(n))
    offs[0] = 0
    out = []
    for i in range(n):
        m = {"author": auths[i % len(auths)], "platform": (plats or PLATS)[i % len(plats or PLATS)], "offsetMs": offs[i], "text": texts[i]}
        if roles:
            m["role"] = roles
        out.append(m)
    return out


def gen_sequences():
    rng = random.Random(9001)
    n = 0
    # raid bursts: >=5 distinct authors, same or near-same text, within 20 s
    for i in range(45):
        base = RAID_BASE[i % len(RAID_BASE)]
        k = 5 + (i % 8)
        texts = [mutate(base, rng, i + j) for j in range(k)]
        au = authors(rng, k)
        seq("raid-%03d" % i, "raid", "hold", "%d distinct authors, near-same text, varied punctuation, casing or lookalikes, inside 20 s" % k,
            msgs(rng, texts, au, 19000, plats=PLATS if i % 2 else ["twitch"]), fire_at=4, kind="burst")
    # raid with the same exact text
    for i in range(10):
        base = RAID_BASE[(i * 3) % len(RAID_BASE)]
        k = 6 + i % 5
        seq("raid-exact-%03d" % i, "raid", "hold", "%d distinct authors, identical text" % k, msgs(rng, [base] * k, authors(rng, k), 15000), fire_at=4, kind="burst")
    # under the line: not a raid
    for i in range(10):
        base = RAID_BASE[i % len(RAID_BASE)]
        k = 4
        seq("raid-few-%03d" % i, "raid", "pass", "only 4 distinct authors (threshold is 5)", msgs(rng, [mutate(base, rng, i + j) for j in range(k)], authors(rng, k), 15000), kind="below_threshold")
    for i in range(10):
        base = RAID_BASE[i % len(RAID_BASE)]
        k = 6
        m = msgs(rng, [mutate(base, rng, i + j) for j in range(k)], authors(rng, k), 19000)
        for j, x in enumerate(m):
            x["offsetMs"] = j * 9000
        seq("raid-slow-%03d" % i, "raid", "pass", "6 authors but spread over 45 s, outside the 20 s window", m, kind="outside_window")
    for i in range(6):
        base = RAID_BASE[i % len(RAID_BASE)]
        seq("raid-moderators-%03d" % i, "raid", "pass", "5+ moderators repeating an announcement (exempt roles)",
            msgs(rng, [base] * 6, authors(rng, 6, "mod"), 12000, roles="moderator"), kind="exempt_role")
    # legitimate crowd chants must NOT fire
    chants = ["F", "GG", "W", "LUL", "Kappa", "o7", "L", "gg", "GGs", "+1", "ez", "KEKW", "Pog", "RIP", "w"]
    for i, c in enumerate(chants):
        k = 8 + i % 8
        seq("chant-%03d" % i, "raid", "pass", "crowd chant %r from %d authors; too short to be a raid" % (c, k), msgs(rng, [c] * k, authors(rng, k), 10000), kind="chant")
    walls = ["Kappa Kappa Kappa Kappa", "LUL LUL LUL LUL LUL LUL", "PogChamp PogChamp PogChamp", "KEKW KEKW KEKW KEKW", "catJAM catJAM catJAM catJAM catJAM",
             "OMEGALUL OMEGALUL OMEGALUL", "Clap Clap Clap Clap Clap Clap", "EZ EZ EZ EZ EZ", "peepoClap peepoClap peepoClap peepoClap", "Sadge Sadge Sadge Sadge"]
    for i, w in enumerate(walls):
        k = 7 + i % 6
        seq("emotewall-%03d" % i, "raid", "pass", "emote-only wall from %d authors, exempt" % k, msgs(rng, [w] * k, authors(rng, k), 12000), kind="emote_only")
    cmds = ["!play", "!lurk", "!discord", "!socials", "!vote 1", "!hype", "!song", "!uptime", "!commands", "!join", "!clip", "!so friend", "!rank", "!points", "!raffle"]
    for i, c in enumerate(cmds):
        k = 6 + i % 7
        seq("command-%03d" % i, "raid", "pass", "bot command %r from %d authors, exempt" % (c, k), msgs(rng, [c] * k, authors(rng, k), 14000), kind="command")
    # near-duplicate single-author sequences
    spam = [
        ["buy now 1", "buy now 2", "buy now 3", "buy now 4", "buy now 5"],
        ["buy now 1/5", "buy now 2/5", "buy now 3/5", "buy now 4/5", "buy now 5/5"],
        ["cheap followers on my page 1", "cheap followers on my page 2", "cheap followers on my page 3", "cheap followers on my page 4"],
        ["best deal today click here", "best deal today click here!", "best deal today, click here", "Best deal today click here", "best deal today click here!!"],
        ["limited offer for you right now", "limited offer for you right now!!", "limited offer for you, right now", "LIMITED offer for you right now"],
        ["order yours today part 1", "order yours today part 2", "order yours today part 3", "order yours today part 4", "order yours today part 5"],
        ["i am selling a great product", "i am selling a really great product", "i am selling a great product now", "i am selling a great product today", "i am selling a great product!"],
        ["win big now 10", "win big now 20", "win big now 30", "win big now 40"],
        ["subscribe to my channel please", "please subscribe to my channel", "subscribe to my channel please!", "subscribe to my channel, please", "please subscribe to my channel!"],
        ["coupon code SAVE 1", "coupon code SAVE 2", "coupon code SAVE 3", "coupon code SAVE 4", "coupon code SAVE 5"],
    ]
    for i in range(30):
        base = spam[i % len(spam)]
        reps = base if i < 10 else [t.upper() if (j + i) % 3 == 0 else t for j, t in enumerate(base)]
        au = authors(rng, 1, "spammer")
        seq("neardup-%03d" % i, "spam_near_duplicate", "flag", "one author, %d near-duplicate messages inside 30 s (repeats > 3, flag only)" % len(reps),
            msgs(rng, reps, au, 25000, plats=[PLATS[i % 3]]), fire_at=3, kind="near_duplicate")
    # numbered giveaway sequences
    for i in range(10):
        word = ["giveaway entry", "raffle ticket", "contest entry", "prize draw", "lucky number"][i % 5]
        reps = ["%s %d" % (word, j) for j in range(1, 6 + i % 3)]
        seq("numbered-%03d" % i, "spam_near_duplicate", "flag", "one author, numbered sequence '%s N' (digit-stripped identical)" % word,
            msgs(rng, reps, authors(rng, 1, "entrant"), 22000, plats=[PLATS[i % 3]]), fire_at=3, kind="numbered")
    # legitimate repeats that must NOT fire
    ok = [["gg", "gg", "gg", "gg", "gg"], ["F", "F", "F", "F"], ["lol", "lol", "lol", "lol"], ["LUL", "LUL", "LUL", "LUL"], ["nice", "nice", "nice", "nice"]]
    for i, t in enumerate(ok):
        seq("shortrepeat-%03d" % i, "spam_near_duplicate", "pass", "one author repeating a very short reaction", msgs(rng, t, authors(rng, 1, "fan"), 20000), kind="short_repeat")
    for i in range(10):
        a = authors(rng, 1, "fan")
        lines = ["that was an amazing play", "who is the best player on the team", "what time does the stream end", "this soundtrack is so good", "i love this boss fight"]
        rng.shuffle(lines)
        seq("distinct-%03d" % i, "spam_near_duplicate", "pass", "one author, five unrelated messages", msgs(rng, lines, a, 25000), kind="distinct")
    for i in range(6):
        lines = ["score is %d-%d" % (j, j + 1) for j in range(3)]
        seq("score-%03d" % i, "spam_near_duplicate", "pass", "one author posting three changing scores (not four, not spam)", msgs(rng, lines, authors(rng, 1, "fan"), 20000), kind="few")
    for i in range(6):
        lines = ["good luck on the run %d" % j for j in range(3)]
        seq("three-%03d" % i, "spam_near_duplicate", "pass", "three near-duplicates is at the limit (> 3 needed)", msgs(rng, lines, authors(rng, 1, "fan"), 20000), kind="at_limit")
    for i in range(6):
        lines = ["good luck on the run"] * 5
        seq("slowrepeat-%03d" % i, "spam_near_duplicate", "pass", "same message five times but over several minutes", [
            {"author": authors(rng, 1, "fan")[0] if False else "fan_%03d" % (i + 1), "platform": "twitch", "offsetMs": j * 90000, "text": t} for j, t in enumerate(lines)], kind="outside_window")


def write_sequences():
    with open(os.path.join(HERE, "sequences.jsonl"), "w", encoding="utf-8", newline="\n") as f:
        f.write(json.dumps({"_header": "Multi-message fixtures for the raid and near-duplicate detectors. expect applies to the sequence; fireAt is the 0-based index of the earliest message that must fire."}) + "\n")
        for r in SEQ:
            f.write(json.dumps(r, ensure_ascii=True, separators=(", ", ": ")) + "\n")


# =================================================================== OUTPUT
def dumps(row):
    """ASCII-escape the lines whose point is an invisible or lookalike character."""
    esc = bool(row.pop("esc", False))
    if row.get("obf") in ("homoglyph", "zwsp", "fullwidth", "zalgo", "mixed") or row.get("detector") in ("zalgo", "lookalike"):
        esc = True
    order = ["text", "category", "group", "expect", "note", "slice", "tier", "detector", "obf", "signal", "jev", "author", "platform", "protects", "lang", "lists"]
    ordered = {k: row[k] for k in order if k in row and row[k] != ""}
    for k in row:
        if k not in ordered and row[k] != "":
            ordered[k] = row[k]
    return json.dumps(ordered, ensure_ascii=esc, separators=(", ", ": "))


def write(path, rows, header):
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        if header:
            f.write(json.dumps({"_header": header}) + "\n")
        for r in rows:
            f.write(dumps(dict(r)) + "\n")


def main():
    gen_hate()
    gen_hardcase()
    gen_doxxing()
    gen_number_traps()
    gen_scams()
    gen_flood()
    gen_impersonation()
    gen_scunthorpe()
    gen_nonen()
    gen_benign()
    gen_sequences()
    write_sequences()
    write(os.path.join(HERE, "corpus-en.jsonl"), MAIN,
          "Synthetic labelled fixtures for the automod pack checker. See targets.json for the label semantics. "
          "No real chat, no real people's data: reserved phone, email and IP ranges only.")
    write(os.path.join(HERE, "corpus-en.positive.jsonl"), POS,
          "WARNING: this file contains offensive language, generated from the pack's own hate terms plus obfuscation variants. "
          "It exists only so the pack checker can be measured. It is not published: the docs site builds from web/** and docs/** only.")
    with open(os.path.join(HERE, "protected-names.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump({"_note": "Fake protected accounts for the impersonation fixtures. role is how the name became protected.", "names": PROTECTED}, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print("main", len(MAIN), "positive", len(POS), file=sys.stderr)


if __name__ == "__main__":
    main()
