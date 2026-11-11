"""catalog_table.snr_filter: the one row filter shared by relorient (P/σP) and the catalog overlay (value/σ, or value alone)."""
import warnings

import numpy as np
import catalog_table as ct

nan = float("nan")


def test_ratio_rule_keeps_ge_and_separates_missing_from_below():
    v = np.array([6.0, 3.0, 2.9, nan, 5.0, 5.0, 5.0, 0.0])
    s = np.array([2.0, 1.0, 1.0, 1.0, nan, 0.0, -1.0, 1.0])
    keep, st = ct.snr_filter(v, s, 3.0)
    assert keep.tolist() == [True, True, False, False, False, False, False, False]
    assert st.tolist() == ["", "", "below", "missing", "missing", "missing", "missing", "below"]


def test_without_sigma_is_value_ge_k_and_nan_is_unknown_not_zero():
    keep, st = ct.snr_filter(np.array([1.0, 0.5, nan, -2.0]), None, 1.0)
    assert keep.tolist() == [True, False, False, False]
    assert st.tolist() == ["", "below", "missing", "below"]
    keep0, st0 = ct.snr_filter(np.array([nan]), None, 0.0)
    assert not keep0[0] and st0[0] == "missing"      # a missing value does not pass even k = 0


def test_inf_is_missing_and_no_warnings():
    with warnings.catch_warnings():
        warnings.simplefilter("error")
        keep, st = ct.snr_filter(np.array([np.inf, 4.0]), np.array([1.0, np.inf]), 1.0)
    assert not keep.any() and st.tolist() == ["missing", "missing"]
