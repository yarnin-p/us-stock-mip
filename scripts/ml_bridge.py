#!/usr/bin/env python3
"""JSON stdin/stdout bridge for the PRD's native ML model families."""

from __future__ import annotations

import json
import os
import sys
import tempfile

import numpy as np


def train(request: dict) -> dict:
    algorithm = request["algorithm"]
    features = np.asarray(request["features"], dtype=np.float64)
    labels = np.asarray(request["labels"], dtype=np.int32)
    rounds = int(request["boost_rounds"])
    learning_rate = float(request["learning_rate"])
    l2 = float(request["l2"])

    if algorithm == "lightgbm_v1":
        import lightgbm as lgb

        minimum_leaf_rows = max(50, len(labels) // 200)
        dataset = lgb.Dataset(features, label=labels)
        model = lgb.train(
            {
                "objective": "binary",
                "learning_rate": learning_rate,
                "lambda_l2": l2,
                "num_leaves": 7,
                "max_depth": 4,
                "min_data_in_leaf": minimum_leaf_rows,
                "min_data_in_bin": 5,
                "min_gain_to_split": 0.05,
                "max_delta_step": 0.5,
                "feature_fraction": 0.9,
                "feature_fraction_seed": 17,
                "bagging_fraction": 0.9,
                "bagging_freq": 1,
                "bagging_seed": 17,
                "feature_pre_filter": False,
                "seed": 17,
                "verbosity": -1,
                "num_threads": 1,
            },
            dataset,
            num_boost_round=rounds,
        )
        artifact = model.model_to_string()
        probabilities = model.predict(features)
    elif algorithm == "xgboost_v1":
        import xgboost as xgb

        matrix = xgb.DMatrix(features, label=labels)
        model = xgb.train(
            {
                "objective": "binary:logistic",
                "eta": learning_rate,
                "lambda": l2,
                "max_depth": 6,
                "min_child_weight": 0,
                "seed": 17,
                "nthread": 1,
            },
            matrix,
            num_boost_round=rounds,
        )
        artifact = bytes(model.save_raw(raw_format="json")).decode("utf-8")
        probabilities = model.predict(matrix)
    elif algorithm == "catboost_v1":
        from catboost import CatBoostClassifier

        model = CatBoostClassifier(
            loss_function="Logloss",
            iterations=rounds,
            learning_rate=learning_rate,
            l2_leaf_reg=max(l2, 1e-9),
            depth=6,
            random_seed=17,
            verbose=False,
            allow_writing_files=False,
        )
        model.fit(features, labels)
        artifact = save_catboost(model)
        probabilities = model.predict_proba(features)[:, 1]
    else:
        raise ValueError(f"unsupported algorithm: {algorithm}")

    return {
        "artifact": artifact,
        "probabilities": probabilities.astype(float).tolist(),
    }


def predict(request: dict) -> dict:
    algorithm = request["algorithm"]
    features = np.asarray(request["features"], dtype=np.float64)
    artifact = request["artifact"]

    if algorithm == "lightgbm_v1":
        import lightgbm as lgb

        model = lgb.Booster(model_str=artifact)
        probabilities = model.predict(features)
    elif algorithm == "xgboost_v1":
        import xgboost as xgb

        model = xgb.Booster()
        model.load_model(bytearray(artifact, "utf-8"))
        probabilities = model.predict(xgb.DMatrix(features))
    elif algorithm == "catboost_v1":
        from catboost import CatBoostClassifier

        model = CatBoostClassifier()
        load_catboost(model, artifact)
        probabilities = model.predict_proba(features)[:, 1]
    else:
        raise ValueError(f"unsupported algorithm: {algorithm}")

    return {"probabilities": np.asarray(probabilities, dtype=float).tolist()}


def save_catboost(model) -> str:
    path = ""
    try:
        with tempfile.NamedTemporaryFile(suffix=".json", delete=False) as file:
            path = file.name
        model.save_model(path, format="json")
        with open(path, "r", encoding="utf-8") as file:
            return file.read()
    finally:
        if path:
            os.unlink(path)


def load_catboost(model, artifact: str) -> None:
    path = ""
    try:
        with tempfile.NamedTemporaryFile(
            suffix=".json", mode="w", encoding="utf-8", delete=False
        ) as file:
            file.write(artifact)
            path = file.name
        model.load_model(path, format="json")
    finally:
        if path:
            os.unlink(path)


def main() -> None:
    request = json.load(sys.stdin)
    action = request.get("action")
    if action == "train":
        response = train(request)
    elif action == "predict":
        response = predict(request)
    else:
        raise ValueError(f"unsupported action: {action}")
    json.dump(response, sys.stdout, separators=(",", ":"), allow_nan=False)


if __name__ == "__main__":
    main()
