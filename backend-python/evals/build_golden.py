"""
Golden dataset + fixture generator.

Builds two committed artifacts from one inline spec:

  datasets/golden/golden_v1.jsonl  - 50 (query, response, context, label) examples
  fixtures/golden_v1_fixtures.json - recorded "LLM" responses replayed by
                                     MockLLMProvider so the full pipeline runs
                                     deterministically in CI

The fixtures simulate a competent-but-imperfect verifier model: a handful of
entries deliberately return wrong verdicts (false positives / false negatives /
UNKNOWN parse-failures) so the golden-tier metrics are non-trivial and any
change to scoring or thresholding shifts them — which is exactly what the
CI regression gate exists to catch.

Regenerate with:  python -m evals.build_golden
"""

import hashlib
import json
from pathlib import Path
from typing import Dict, List, Optional

EVALS_DIR = Path(__file__).parent
DATASET_PATH = EVALS_DIR / "datasets" / "golden" / "golden_v1.jsonl"
FIXTURES_PATH = EVALS_DIR / "fixtures" / "golden_v1_fixtures.json"

# ---------------------------------------------------------------------------
# Base facts: each yields a faithful and a hallucinated example.
# ---------------------------------------------------------------------------
FACTS = [
    # id, domain, query, context sentence, true claim, false claim
    (
        "geo-paris",
        "geography",
        "What is the capital of France?",
        "Paris is the capital and largest city of France.",
        "Paris is the capital of France.",
        "Lyon is the capital of France.",
    ),
    (
        "geo-canberra",
        "geography",
        "What is the capital of Australia?",
        "Canberra is the capital city of Australia, chosen as a compromise between Sydney and Melbourne.",
        "Canberra is the capital of Australia.",
        "Sydney is the capital of Australia.",
    ),
    (
        "geo-nile",
        "geography",
        "What is the longest river in the world?",
        "The Nile is generally regarded as the longest river in the world, flowing about 6,650 km through northeastern Africa.",
        "The Nile is regarded as the longest river in the world.",
        "The Nile flows through South America.",
    ),
    (
        "geo-everest",
        "geography",
        "What is the highest mountain on Earth?",
        "Mount Everest, at 8,849 metres above sea level, is Earth's highest mountain.",
        "Mount Everest is Earth's highest mountain.",
        "Mount Everest is approximately 7,000 metres tall.",
    ),
    (
        "geo-tokyo",
        "geography",
        "What is the capital of Japan?",
        "Tokyo is the capital of Japan and the most populous metropolitan area in the world.",
        "Tokyo is the capital of Japan.",
        "Kyoto is the current capital of Japan.",
    ),
    (
        "sci-water",
        "science",
        "At what temperature does water boil?",
        "Water boils at 100 degrees Celsius at standard atmospheric pressure.",
        "Water boils at 100 degrees Celsius at standard pressure.",
        "Water boils at 150 degrees Celsius at standard pressure.",
    ),
    (
        "sci-light",
        "science",
        "How fast does light travel?",
        "The speed of light in a vacuum is exactly 299,792,458 metres per second.",
        "The speed of light in a vacuum is about 299,792 kilometres per second.",
        "The speed of light in a vacuum is about 150,000 kilometres per second.",
    ),
    (
        "sci-dna",
        "science",
        "What is the structure of DNA?",
        "DNA carries genetic information and is structured as a double helix, as described by Watson and Crick in 1953.",
        "DNA is structured as a double helix.",
        "DNA was first described as a triple helix in 1953.",
    ),
    (
        "sci-photo",
        "science",
        "What does photosynthesis produce?",
        "Photosynthesis converts carbon dioxide and water into glucose and oxygen using light energy.",
        "Photosynthesis produces oxygen.",
        "Photosynthesis consumes oxygen and produces carbon dioxide.",
    ),
    (
        "sci-moon-gravity",
        "science",
        "How strong is gravity on the Moon?",
        "The Moon's surface gravity is about 1.62 m/s2, roughly one-sixth of Earth's.",
        "The Moon's gravity is about one-sixth of Earth's.",
        "The Moon's gravity is stronger than Earth's.",
    ),
    (
        "his-apollo",
        "history",
        "When did humans first land on the Moon?",
        "Apollo 11 landed the first humans on the Moon on July 20, 1969.",
        "Apollo 11 landed humans on the Moon in 1969.",
        "Apollo 11 landed humans on the Moon in 1972.",
    ),
    (
        "his-ww2",
        "history",
        "When did World War II end?",
        "World War II ended in 1945 with the surrender of Germany in May and Japan in September.",
        "World War II ended in 1945.",
        "World War II ended in 1950.",
    ),
    (
        "his-press",
        "history",
        "Who introduced the printing press in Europe?",
        "Johannes Gutenberg introduced the movable-type printing press in Europe around 1440.",
        "Gutenberg introduced the movable-type printing press in Europe.",
        "Gutenberg invented the telephone in 1440.",
    ),
    (
        "his-rome",
        "history",
        "When was Rome founded?",
        "According to tradition, the city of Rome was founded in 753 BC.",
        "Rome was traditionally founded in 753 BC.",
        "Rome was founded in 1200 AD.",
    ),
    (
        "tech-python",
        "technology",
        "Who created the Python language?",
        "Python was created by Guido van Rossum and first released in 1991.",
        "Python was first released in 1991.",
        "Python was created by Linus Torvalds.",
    ),
    (
        "tech-www",
        "technology",
        "Who invented the World Wide Web?",
        "Tim Berners-Lee invented the World Wide Web in 1989 while working at CERN.",
        "Tim Berners-Lee invented the World Wide Web.",
        "The World Wide Web was invented by Bill Gates.",
    ),
    (
        "tech-transistor",
        "technology",
        "Where was the transistor invented?",
        "The transistor was invented at Bell Labs in 1947.",
        "The transistor was invented at Bell Labs.",
        "The transistor was invented by NASA in 1969.",
    ),
    (
        "tech-linux",
        "technology",
        "When was the Linux kernel first released?",
        "The Linux kernel was first released by Linus Torvalds in 1991.",
        "The Linux kernel was first released in 1991.",
        "The Linux kernel was first released by Steve Jobs.",
    ),
    (
        "med-heart",
        "medicine",
        "How many chambers does the human heart have?",
        "The human heart has four chambers: two atria and two ventricles.",
        "The human heart has four chambers.",
        "The human heart has six chambers.",
    ),
    (
        "med-penicillin",
        "medicine",
        "Who discovered penicillin?",
        "Alexander Fleming discovered penicillin in 1928.",
        "Penicillin was discovered by Alexander Fleming.",
        "Penicillin was discovered by Marie Curie.",
    ),
    (
        "med-insulin",
        "medicine",
        "Which organ produces insulin?",
        "Insulin is a hormone produced by the pancreas that regulates blood glucose levels.",
        "Insulin is produced by the pancreas.",
        "Insulin is produced by the liver.",
    ),
    (
        "med-bones",
        "medicine",
        "How many bones are in the adult human body?",
        "An adult human skeleton typically has 206 bones.",
        "An adult human skeleton has about 206 bones.",
        "An adult human skeleton has exactly 500 bones.",
    ),
]

# Simulated model errors (the "recorded model" gets these wrong on purpose).
# Maps example id -> forced verifier verdict for its claim.
FORCED_VERDICTS: Dict[str, Dict[str, object]] = {
    # False negatives: hallucinated examples the recorded verifier misses.
    "his-rome-hallucinated": {"status": "SUPPORTED", "confidence": 0.55},
    "med-bones-hallucinated": {"status": "SUPPORTED", "confidence": 0.6},
    # False positives: faithful examples the recorded verifier wrongly rejects.
    "tech-linux-faithful": {"status": "UNSUPPORTED", "confidence": 0.8},
    "sci-moon-gravity-faithful": {"status": "UNSUPPORTED", "confidence": 0.78},
    # Parse-failure simulation: verifier returns UNKNOWN with zero confidence.
    "med-insulin-faithful": {"status": "UNKNOWN", "confidence": 0.0},
}

# Multi-claim special cases (explicit claims with per-claim verdicts).
SPECIALS = [
    {
        "id": "special-eiffel-mixed",
        "domain": "mixed",
        "query": "Tell me about the Eiffel Tower.",
        "context": [
            "The Eiffel Tower was completed in 1889 in Paris and is about 330 metres tall."
        ],
        "label_hallucinated": True,
        "claims": [
            ("The Eiffel Tower is located in Paris.", "SUPPORTED", 0.95),
            ("The Eiffel Tower was completed in 1789.", "UNSUPPORTED", 0.9),
        ],
    },
    {
        "id": "special-shakespeare-mixed",
        "domain": "mixed",
        "query": "Who wrote Romeo and Juliet, and when was it published?",
        "context": [
            "William Shakespeare wrote the tragedy Romeo and Juliet, first published in 1597."
        ],
        "label_hallucinated": True,
        "claims": [
            ("Romeo and Juliet was written by William Shakespeare.", "SUPPORTED", 0.93),
            ("Romeo and Juliet was first published in 1850.", "UNSUPPORTED", 0.88),
        ],
    },
    {
        "id": "special-greatwall-partial",
        "domain": "mixed",
        "query": "How long is the Great Wall of China, and when was it built?",
        "context": [
            "The Great Wall of China, built across many dynasties, measures roughly 21,196 km including all branches."
        ],
        "label_hallucinated": True,
        "claims": [
            (
                "The Great Wall of China is about 21,000 km long including all its branches.",
                "SUPPORTED",
                0.85,
            ),
            (
                "The Great Wall of China was built during a single dynasty.",
                "PARTIALLY_SUPPORTED",
                0.6,
            ),
        ],
    },
    {
        # Boundary case: 1 of 3 claims partially supported (ratio 0.33 > 0.3
        # threshold) flags a response that is labeled faithful — a deliberate
        # false positive demonstrating threshold sensitivity.
        "id": "special-bees-boundary",
        "domain": "mixed",
        "query": "Tell me about honey bees.",
        "context": [
            "Honey bees live in colonies with a single queen, communicate through waggle dances, and produce honey from nectar."
        ],
        "label_hallucinated": False,
        "claims": [
            ("Honey bees live in colonies.", "SUPPORTED", 0.95),
            ("Honey bees produce honey from nectar.", "SUPPORTED", 0.9),
            (
                "Honey bee colonies often have several active queens at once.",
                "PARTIALLY_SUPPORTED",
                0.5,
            ),
        ],
    },
    {
        "id": "special-jupiter-faithful",
        "domain": "mixed",
        "query": "Describe the planet Jupiter.",
        "context": [
            "Jupiter is the largest planet in the Solar System, a gas giant with a prominent storm called the Great Red Spot."
        ],
        "label_hallucinated": False,
        "claims": [
            ("Jupiter is the largest planet in the Solar System.", "SUPPORTED", 0.97),
            ("Jupiter is a gas giant.", "SUPPORTED", 0.94),
            ("Jupiter has a storm called the Great Red Spot.", "SUPPORTED", 0.92),
        ],
    },
    {
        "id": "special-mars-hallucinated",
        "domain": "mixed",
        "query": "Describe the planet Mars.",
        "context": [
            "Mars is the fourth planet from the Sun, known as the Red Planet, with two small moons: Phobos and Deimos."
        ],
        "label_hallucinated": True,
        "claims": [
            ("Mars is the second planet from the Sun.", "UNSUPPORTED", 0.9),
            ("Mars has fifteen moons.", "UNSUPPORTED", 0.93),
        ],
    },
]


def _stable_confidence(key: str, low: float, high: float) -> float:
    """Deterministic pseudo-random confidence derived from the example id."""
    digest = hashlib.md5(key.encode("utf-8")).hexdigest()
    fraction = int(digest[:8], 16) / 0xFFFFFFFF
    return round(low + fraction * (high - low), 2)


def _verifier_response(status: str, confidence: float, evidence: Optional[List[str]]) -> str:
    return json.dumps(
        {
            "status": status,
            "confidence": confidence,
            "evidence": evidence or [],
            "reasoning": "Recorded verdict from the golden fixture set.",
        }
    )


def build() -> None:
    examples = []
    fixtures = []
    seen_claims: set[str] = set()

    def add_example(
        example_id: str,
        domain: str,
        query: str,
        context: List[str],
        claims: List[tuple],
        label_hallucinated: bool,
    ) -> None:
        claim_texts = [claim for claim, _, _ in claims]
        for claim in claim_texts:
            if claim in seen_claims:
                raise ValueError(f"Claim text reused across examples: {claim!r}")
            seen_claims.add(claim)

        response = " ".join(claim_texts)
        examples.append(
            {
                "id": example_id,
                "domain": domain,
                "query": query,
                "response": response,
                "context": context,
                "label_hallucinated": label_hallucinated,
            }
        )
        # Decomposer fixture: keyed on the decomposer instruction + the full
        # response text (which appears verbatim inside <text>...</text>).
        fixtures.append(
            {
                "match": ["Extract all factual claims", response],
                "response": json.dumps(claim_texts),
            }
        )
        # Verifier fixtures: keyed on the exact <claim> block.
        for claim, status, confidence in claims:
            evidence = [context[0]] if status == "SUPPORTED" else []
            fixtures.append(
                {
                    "match": [f"<claim>\n{claim}\n</claim>"],
                    "response": _verifier_response(status, confidence, evidence),
                }
            )

    for fact_id, domain, query, context_sentence, true_claim, false_claim in FACTS:
        for variant, claim, label in (
            ("faithful", true_claim, False),
            ("hallucinated", false_claim, True),
        ):
            example_id = f"{fact_id}-{variant}"
            forced = FORCED_VERDICTS.get(example_id)
            if forced:
                status, confidence = str(forced["status"]), float(forced["confidence"])  # type: ignore[arg-type]
            elif label:
                status = "UNSUPPORTED"
                confidence = _stable_confidence(example_id, 0.75, 0.97)
            else:
                status = "SUPPORTED"
                confidence = _stable_confidence(example_id, 0.82, 0.98)
            add_example(
                example_id,
                domain,
                query,
                [context_sentence],
                [(claim, status, confidence)],
                label,
            )

    for special in SPECIALS:
        add_example(
            str(special["id"]),
            str(special["domain"]),
            str(special["query"]),
            list(special["context"]),  # type: ignore[arg-type]
            list(special["claims"]),  # type: ignore[arg-type]
            bool(special["label_hallucinated"]),
        )

    DATASET_PATH.parent.mkdir(parents=True, exist_ok=True)
    FIXTURES_PATH.parent.mkdir(parents=True, exist_ok=True)

    with open(DATASET_PATH, "w", encoding="utf-8", newline="\n") as f:
        for example in examples:
            f.write(json.dumps(example) + "\n")

    with open(FIXTURES_PATH, "w", encoding="utf-8", newline="\n") as f:
        json.dump(fixtures, f, indent=2)
        f.write("\n")

    hallucinated = sum(1 for e in examples if e["label_hallucinated"])
    print(f"Wrote {len(examples)} examples ({hallucinated} hallucinated) -> {DATASET_PATH}")
    print(f"Wrote {len(fixtures)} fixtures -> {FIXTURES_PATH}")


if __name__ == "__main__":
    build()
